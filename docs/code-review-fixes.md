# Go 코드리뷰 후속 조치

## 범위
- 저장소: `apps/rpa`
- 기준: 코드리뷰 결과(Critical 1건, Warning 2건)

## 작업 목록
- [x] 1. `supervisor` 종료 경로의 `exec.Cmd.Wait` 중복 호출 경쟁 조건 제거
  - 파일: `apps/rpa/internal/supervisor/supervisor.go`
  - 상태: 완료

- [x] 2. 런타임 IPC 업데이트 실패 시 CLI 종료코드가 성공(0)으로 떨어지는 문제 수정
  - 파일: `apps/rpa/internal/cli/cli.go`
  - 상태: 완료

- [x] 3. `Logger.SetLevel` 동시성 data race 가능성 제거
  - 파일: `apps/rpa/pkg/logging/logs.go`
  - 상태: 완료

## 검증 결과
- [x] `go test ./...` 통과 (테스트 파일 없음)
- [x] `go vet ./...` 통과

## 진행 로그
- [x] 1번 수정 및 문서 반영 완료
- [x] 2번 수정 및 문서 반영 완료
- [x] 3번 수정 및 문서 반영 완료
- [x] 추가 보완: `StateConnected` 전이 실패 타임아웃 시 ssh 프로세스 강제 종료 보장
  - 파일: `apps/rpa/internal/supervisor/supervisor.go`
