# 배포 및 운영 관련 문서

## 1. K8s 배포 전략

### 1.1 상시 서비스 4종 (Deployment)

backend/orderapi/matching-engine/recorder 4개는 항상 떠 있는 Deployment다. 4개 전부 `imagePullPolicy: Always`를 명시적으로 지정한다 — 이미지 태그가 `{module}-latest` 고정이라 K8s 기본값(IfNotPresent)을 쓰면 재배포해도 캐시된 예전 이미지를 계속 쓰는 문제가 있었기 때문이다.

### 1.2 온디맨드 Job (Fargate)

trader/replayengine은 `-date` 실행마다 한 번 뜨고 완료 후 종료되는 K8s Job이라 상시 대기 노드가 필요 없다. replayengine은 K8s Indexed Job(`completionMode: Indexed`)으로 `-shard-index`를 자동 분배한다.

## 2. 오토스케일링

### 2.1 Karpenter NodePool 2종

| NodePool | 인스턴스 타입 | 대상 | CPU 한도 |
|---|---|---|---|
| team1-backend | c6i.large 고정 | orderapi · matching-engine (관리형 노드그룹과 별도) | 제한 없음(온디맨드) |
| team1-recorder | c6i.xlarge 고정 | recorder 전용 | 8 vCPU (c6i.xlarge 최대 2대 상당) |

recorder가 60배속 세션 종료 시점 체결 배치를 처리하려면 c6i.large의 allocatable을 거의 다 쓰는 2.5Gi가 필요해, recorder만 별도 NodePool(c6i.xlarge, 5Gi)로 분리했다. 두 NodePool 모두 `consolidateAfter`를 5분→2분으로 단축(스케일다운 체감 속도 개선).

### 2.2 KEDA ScaledObject

| 대상 | 트리거 지표 | 임계값 | 비고 |
|---|---|---|---|
| matching-engine | sum(matching_engine_lag) | 500 | 컨슈머 랙 — 처리 속도 자체가 밀리는 상황 포착 |
| matching-engine | sum(matching_engine_book_size) | 20000 | 랙은 0인데 미체결 주문이 메모리에 쌓여 OOM되는 상황을 랙 지표가 못 잡아서 추가 |
| recorder | sum(recorder_consumer_lag) | 1000 | recorder 자체 백프레셔 상한(2000)의 절반 — 429가 실제로 뜨기 전에 먼저 스케일아웃 시도 |

두 서비스 모두 CPU가 아니라 Prometheus 지표(자체 호스팅 EC2)로 스케일한다 — 부하가 CPU보다 "처리 속도 대비 유입 속도"에서 먼저 드러나기 때문이다. matching-engine은 두 트리거 중 더 많은 레플리카를 요구하는 쪽을 따른다(OR 조건).

## 3. 모니터링 & 알림

### 3.1 자체 호스팅 Prometheus + Grafana

원래 in-cluster kube-prometheus-stack이었으나, AMP/AMG가 조직 IAM 권한 부족으로 구조적으로 막혀 있어 자체 호스팅 EC2(monitoring-ec2.tf)로 이전했다. KEDA ScaledObject 2종이 여기를 지표 소스로 쓰고, Grafana 대시보드(team1-overview.json)가 시스템 전반을 시각화한다.

### 3.2 CloudWatch 알람 5종 + SNS

| 알람 | 지표 | 임계값 | 평가 방식 |
|---|---|---|---|
| team1-alarm-redis-cpu-* | AWS/ElastiCache EngineCPUUtilization | 80% 초과 | 노드별(replication_group_id-00N), 5분 평균 연속 5회 |
| team1-alarm-msk-health | AWS/Kafka SumOffsetLag | 100000 초과 | 클러스터 단위 컨슈머 그룹 랙 최댓값, 연속 5회 |
| team1-alarm-alb-5xx-* | AWS/ApplicationELB HTTPCode_Target_5XX_Count | 50 초과 | ALB별(orderapi/collector), 1분 합계 연속 5회 |
| team1-alarm-nodegroup-health | ContainerInsights cluster_failed_node_count | 0 초과 | EKS 클러스터 단위, 연속 5회 |
| team1-alarm-mysql-ec2-cpu | AWS/EC2 CPUUtilization | MySQL EC2 인스턴스 | 자체 호스팅 MySQL 인스턴스 CPU 감시 |

전부 SNS(team1-sns-alerts) 하나로 발행된다. alb_5xx/redis_cpu 모두 처음엔 아직 존재하지 않는 리소스를 기준으로 차원을 잡아둬 사실상 한 번도 안 울리는 상태였다가, 실제 리소스가 생긴 뒤 정정했다.

## 4. 운영 절차 (런북)

### 4.1 야간 비용 절감 destroy → 익일 복구

비용 절감을 위해 밤에 EKS 클러스터를 통째로 지웠다가 다음 날 다시 올리는 걸 반복 검증했다 — 복구는 사람이 수동으로 트리거하며 인프라부터 K8s 리소스까지 자동으로 재구성된다. MySQL은 RDS가 아니라 EC2+EBS라 destroy 시 데이터가 스냅샷 없이 사라진다 — 디스포저블 테스트 데이터라 통상 문제는 없지만, 보존이 필요하면 별도 백업 절차가 필요하다.

### 4.2 MySQL 스키마 적용

recorder는 스키마를 자동 마이그레이션하지 않는다(마이그레이션 툴 없음, 저장소 컨벤션). RDS를 삭제하고 자체 호스팅 MySQL EC2로 전환하면서 recorder/schema.sql을 컨테이너의 `/docker-entrypoint-initdb.d/`에 올려둬 최초 기동 시 자동 적용되도록 했다 — `CREATE TABLE IF NOT EXISTS` / `CALL create_index_if_absent(...)`로만 되어 있어 재실행해도 안전하다(멱등, 검증 완료).

### 4.3 세션가드 · 중지 버튼

orderapi의 세션 API가 trader/replayengine이 동시에 두 개 이상 도는 것을 막는다 — 두 프로세스 모두 시작 시 세션을 클레임하고 하트비트로 유지한다. 프론트엔드의 중지 버튼은 이 하트비트 응답에 `stopRequested` 플래그를 실어 그레이스풀하게 멈춘다 — 남아있는 미종결 주문은 세션 종료 시 실제 CANCEL 이벤트로 자동 정리된다(삭제 아님).
