# 리소스 사용량 백업 — 2026-08-26 ~ 2026-08-27

인프라 전체 재구축(2026-08-26 09:25 KST) 직후부터 수집 시점(2026-08-27 18:14 KST)까지,
실제 테스트/실험 구간의 CPU·메모리 실측치를 모두 백업. 나중에 PPT 성능검증 슬라이드에
그대로 쓸 수 있도록 원본(raw) + 가공본(CSV) + 요약 수치를 함께 남김.

## 수집 방법

- **EC2/ElastiCache**: `aws cloudwatch get-metric-data` (5분 간격, 2026-08-25T15:00Z~수집시점 UTC)
- **K8s 노드/파드 단위**: 자체 구축 Prometheus(`monitor.jhyang.click:9090`, node-exporter + cAdvisor,
  보존기간 3일) `query_range` API 직접 호출 (5분 간격)

## 파일 목록

| 파일 | 내용 |
|---|---|
| `ec2-redis-cpu-mem-20260826-27.csv` | EC2 8대 + Redis 2대 CPU%, Redis 메모리%(5분 간격 시계열) |
| `app-pod-cpu-mem-hourly-20260826-27.csv` | matching-engine/recorder/orderapi 파드 CPU·메모리(전체 replica 합, 시간별 avg/max) |
| `cloudwatch-raw.json` | 위 CloudWatch 쿼리의 원본 응답(재가공 필요시 사용) |
| `prometheus-raw.json` | 위 Prometheus query_range 원본 응답(파드별 개별 시계열 포함, 재가공 필요시 사용) |

## 핵심 수치 요약 (구간 전체 avg / max)

### EC2 CPU%
| 인스턴스 | avg | max |
|---|---|---|
| team1-mysql (m6i.2xlarge) | 18.5% | **99.8%** |
| team1-monitoring (t3.medium) | 5.5% | 28.2% |
| eks-node-system ×2 (t3.medium) | 4.6~7.2% | 11~12% |
| karpenter-backend 노드 (c6i.large) | 4.4~10.1% | 18.8~37.8% |
| karpenter-recorder 노드 (c6i.xlarge) | 3.0% | 6.8% |

### Redis (ElastiCache)
| | avg | max |
|---|---|---|
| redis-001 EngineCPU% | 28.3% | **99.9%** |
| redis-002 EngineCPU% | 10.3% | 63.0% |
| redis-001 메모리% | 1.4% | 5.7% |
| redis-002 메모리% | 0.9% | 1.8% |

→ **redis-001은 burstable 인스턴스(cache.t4g.medium)에서 CPU가 두 차례 이상 99%대까지 치솟음.**
메모리 여유는 충분(<6%)하므로 병목은 CPU/CPU 크레딧 쪽 — PPT 트러블슈팅/개선항목에 근거자료로 쓸 수 있음.

### K8s 워크로드 파드 (전체 replica 합산)
| 워크로드 | CPU avg | CPU max | 메모리 avg | 메모리 max |
|---|---|---|---|---|
| matching-engine | 0.363 core | 2.073 core | 314 MB | 1,421 MB |
| recorder | 0.049 core | 0.086 core | 92 MB | **1,022 MB** |
| orderapi | 0.101 core | 0.456 core | 1,102 MB | 2,982 MB |

→ recorder는 평시 메모리(92MB)와 최대치(1,022MB) 격차가 10배 이상 — `af6e4cc`/`bc523c5` 커밋에서
기록한 "고배속 리플레이 종료 시점 버스트로 인한 OOM" 패턴이 이번 구간 실측에서도 그대로 나타남.

### K8s 노드 메모리 사용률 (system+backend+recorder 노드 전체, 3,613개 데이터포인트)
avg **26.3%**, max **94.3%**, min 9.3%

## 알려진 모니터링 공백 (참고)

- **team1-mysql, team1-monitoring EC2의 메모리 사용률은 수집 불가** — 이 두 인스턴스는 K8s 노드가 아니라서
  node-exporter가 안 붙고, CWAgent도 설치돼 있지 않음(`aws cloudwatch list-metrics --namespace CWAgent` 결과 0건).
  CPU만 CloudWatch 기본 지표로 확인 가능. 메모리까지 보려면 CWAgent 설치가 필요(현재 미설치 상태 그대로 기록).
