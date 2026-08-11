// Package session은 orderapi의 세션 API(POST/PUT/DELETE /v1/sessions)를 호출해
// "트레이더/시뮬레이터는 동시에 하나만 실행돼야 한다"는 팀 결정(2026-08-06)을
// 지킵니다 — 두 개 이상의 트레이더, 또는 트레이더와 리플레이 엔진이 동시에
// 실행되면 같은 매칭 엔진 호가창에 서로 다른 실행의 주문이 섞여 들어가는
// 상황(undefined로 결론)을 실행 시작 시점에 막습니다.
//
// 세션은 실행이 시작될 때 딱 한 번만 클레임합니다 — 주문 하나하나가 오가는
// 경로(HTTPOrderSubmitter 쪽)에는 이 패키지가 전혀 관여하지 않으므로 성능에
// 영향이 없습니다. trader/session과 같은 이유로 독립적으로 다시 선언합니다
// (모듈 간 타입 비공유 원칙).
package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// ErrAlreadyActive는 이미 다른 세션이 활성 상태일 때 Claim이 반환합니다.
var ErrAlreadyActive = errors.New("이미 활성 세션이 있습니다")

// claimRequest의 RunID는 FR-19 분산 실행 지원용입니다 — 같은 리플레이 실행에
// 속한 여러 샤드가 전부 같은 값을 보내면 세션 가드를 하나의 그룹으로 함께
// 통과합니다(`orderapi/session`의 그룹 모델 참고). 비우면(샤드 1개짜리 단독
// 실행) 서버가 하나 생성해 그룹 크기 1로 취급 — 기존 동작과 동일합니다.
type claimRequest struct {
	Owner string `json:"owner"`
	RunID string `json:"runId,omitempty"`
}

type claimResponse struct {
	SessionID  string `json:"sessionId"`
	RunID      string `json:"runId"`
	Owner      string `json:"owner"`
	ClaimedAt  string `json:"claimedAt"`
	TTLSeconds int    `json:"ttlSeconds"`
}

type errorResponse struct {
	ErrorCode string `json:"errorCode"`
	Message   string `json:"message"`
}

// Client는 orderapi의 세션 API를 호출합니다.
type Client struct {
	HTTPClient *http.Client
	BaseURL    string
}

// Claim은 owner 이름으로 runID 그룹에 합류합니다(runID가 비어있으면 그룹
// 크기 1로 단독 클레임). 이미 다른 runID/owner의 그룹이 활성 상태면
// ErrAlreadyActive를 감싼 에러를 반환합니다 — 호출부는 이 경우 실행을 시작하지
// 말고 종료해야 합니다.
func (c Client) Claim(ctx context.Context, owner, runID string) (sessionID string, ttlSeconds int, err error) {
	body, err := json.Marshal(claimRequest{Owner: owner, RunID: runID})
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/sessions", bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusConflict {
		var errResp errorResponse
		json.Unmarshal(respBody, &errResp)
		return "", 0, fmt.Errorf("%w: %s", ErrAlreadyActive, errResp.Message)
	}
	if resp.StatusCode != http.StatusCreated {
		return "", 0, fmt.Errorf("세션 클레임 실패 (status=%d): %s", resp.StatusCode, respBody)
	}

	var claimed claimResponse
	if err := json.Unmarshal(respBody, &claimed); err != nil {
		return "", 0, fmt.Errorf("세션 클레임 응답 파싱 실패: %w", err)
	}
	return claimed.SessionID, claimed.TTLSeconds, nil
}

// Heartbeat는 세션 TTL을 갱신합니다.
func (c Client) Heartbeat(ctx context.Context, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+"/v1/sessions/"+sessionID+"/heartbeat", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body) // 커넥션 재사용을 위해 끝까지 읽음(submitter.go와 같은 이유)

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("세션 하트비트 실패 (status=%d)", resp.StatusCode)
	}
	return nil
}

// Release는 세션을 명시적으로 반납합니다. 정상 종료 경로에서 호출합니다 —
// 크래시로 못 부르면 TTL이 지나서 자동으로 풀립니다(자기치유).
func (c Client) Release(ctx context.Context, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/v1/sessions/"+sessionID, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	// 이미 없어진 세션(404)을 반납하려는 것도 실질적으로는 성공(더 이상 아무도 이
	// 세션을 안 들고 있음)이라 에러로 취급하지 않습니다.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("세션 반납 실패 (status=%d)", resp.StatusCode)
	}
	return nil
}

// RunHeartbeat는 ctx가 끝날 때까지 주기적으로 Heartbeat를 호출합니다. 간격을
// ttlSeconds의 1/3로 잡는 이유는, 네트워크 지연이나 한두 번의 실패로 곧바로
// 세션이 만료되지 않을 여유를 두기 위함입니다.
func RunHeartbeat(ctx context.Context, c Client, sessionID string, ttlSeconds int) {
	interval := time.Duration(ttlSeconds) * time.Second / 3
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Heartbeat(ctx, sessionID); err != nil {
				log.Printf("세션 하트비트 실패 (sessionId=%s): %v", sessionID, err)
			}
		}
	}
}
