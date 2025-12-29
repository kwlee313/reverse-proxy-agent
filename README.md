# Reverse Proxy Agent (rpa)

rpa는 macOS에서 SSH 터널(원격 포워드/로컬 포워드)을 안정적으로 유지하기 위한 CLI입니다.
launchd 서비스 모드와 포그라운드 실행을 모두 지원하고, sleep/network 이벤트에 따라 자동 재시작합니다.

## 기능

- Agent(원격 포워드) 모드: `agent up/down/run`, 동적 포워드 add/remove/clear
- Client(로컬 포워드) 모드: `client up/down/run/add/remove/clear`, 진단 `client doctor`
- launchd 서비스 모드 지원
- JSON 로그, status/metrics 제공
- 설정 파일 기반 운영

## 왜 rpa인가

rpa는 SSH 자체를 대체하려는 도구가 아니라, **운영 환경에서 “SSH 터널을 항상 살아 있게 유지”해야 하는 요구**에 최적화된 실행 도구입니다.
일회성 터널을 여는 것보다, *끊김·슬립·네트워크 변경이 반복되는 환경*에서 터널이 안정적으로 유지되는 것이 더 중요할 때 특히 효과적입니다.

다음과 같은 상황에서 유즈케이스가 특히 강합니다.

- 운영 중인 서버/DB에 **항상 열려 있는 터널**이 필요하고, 사용자는 수동 복구에 관여하고 싶지 않을 때
- macOS 환경에서 **launchd 기반 상시 서비스**로 터널을 관리하고 싶을 때
- sleep/wake, Wi‑Fi/VPN 전환 등 **로컬 환경 변동**이 많아도 자동 복구가 보장돼야 할 때
- “무엇이 왜 끊겼는지”를 **status/logs/metrics만으로 설명 가능한 상태**가 필요할 때

다른 프로젝트 대비 rpa가 필요한 이유는 다음과 같습니다.

- **운영 친화성에 집중**: 단순한 포워딩 기능보다 “끊기지 않는 터널”을 우선 목표로 설계
- **관측 가능성 내장**: 상태/지표/로그가 기본 제공되어 운영자가 상황을 빠르게 판단 가능
- **macOS 최적화**: launchd 기반으로 설치/실행 흐름을 일관되게 제공
- **불필요한 복잡성 제거**: 별도 중앙 서버 애플리케이션 없이 SSH만으로 동작

즉, rpa는 “어떻게 터널을 여느냐”보다 **“운영 환경에서 어떻게 안정적으로 유지하느냐”**에 집중한 도구입니다.

## 중앙 서버(원격 SSH 서버) 준비

이 프로젝트에 별도의 “중앙 서버” 애플리케이션은 없습니다. 대신 **SSH 서버**가 필요합니다.
아래는 일반적인 Linux 서버 기준 설정 예시입니다.

1) SSH 서버 설치/활성화
```sh
sudo apt-get update
sudo apt-get install -y openssh-server
sudo systemctl enable --now ssh
```

2) 포워딩 허용 설정
`/etc/ssh/sshd_config`에서 다음 항목을 확인/설정한 뒤 재시작하세요.
```
AllowTcpForwarding yes
GatewayPorts yes
```
```sh
sudo systemctl restart ssh
```

3) 방화벽/보안그룹
- 원격 포워드로 열려는 포트가 외부에서 접근 가능하도록 방화벽을 열어야 합니다.
- AWS/GCP 등에서는 보안그룹 인바운드 규칙도 확인하세요.

4) 키 기반 접속
- 로컬에서 `ssh user@host`가 비밀번호 없이 접속되는지 확인하세요.

## 설치

### GitHub Releases (권장)
```sh
curl -L -o rpa https://github.com/<owner>/<repo>/releases/latest/download/rpa_<version>_darwin_arm64
chmod +x rpa
mv rpa /usr/local/bin/
```

### 소스에서 빌드
```sh
go build -o rpa ./cmd/rpa
```

## 빠른 시작

### Agent (원격 포워드)
```sh
rpa init \
  --ssh-user ubuntu \
  --ssh-host example.com \
  --remote-forward "0.0.0.0:2222:localhost:22"

rpa agent up
rpa status
rpa logs --follow
rpa agent down
```

### Client (로컬 포워드, init 후 한 줄 실행)
```sh
rpa init \
  --ssh-user ubuntu \
  --ssh-host example.com \
  --local-forward "127.0.0.1:15432:127.0.0.1:5432"

rpa client run
```

### Client (서비스 모드)
```sh
rpa client up
rpa client status
rpa client logs
rpa client metrics
rpa client down
```

## 명령어

```
rpa init --ssh-user user --ssh-host host --remote-forward spec [--config path]
rpa init --ssh-user user --ssh-host host --local-forward spec [--config path]

rpa agent up --config path
rpa agent down --config path
rpa agent run --config path
rpa agent add --remote-forward spec --config path
rpa agent remove --remote-forward spec --config path
rpa agent clear --config path

rpa client up --config path [--local-forward spec]
rpa client down --config path
rpa client run --config path [--local-forward spec]
rpa client add --local-forward spec --config path
rpa client remove --local-forward spec --config path
rpa client clear --config path
rpa client status --config path
rpa client logs --config path
rpa client metrics --config path
rpa client doctor --config path [--local-forward spec]

rpa status --config path
rpa logs --config path [--follow]
rpa metrics --config path
```

## 설정 파일

기본 경로: `~/.rpa/rpa.yaml` (`--config` 또는 `RPA_CONFIG`로 변경 가능)

```yaml
agent:
  name: "rpa-agent"
  launchd_label: "com.rpa.agent"
  restart_policy: "always"
  restart:
    min_delay_ms: 2000
    max_delay_ms: 30000
    factor: 2.0
    jitter: 0.2
    debounce_ms: 2000
  periodic_restart_sec: 3600
  sleep_check_sec: 5
  sleep_gap_sec: 30
  network_poll_sec: 5

client:
  name: "rpa-client"
  launchd_label: "com.rpa.client"
  restart_policy: "always"
  restart:
    min_delay_ms: 2000
    max_delay_ms: 30000
    factor: 2.0
    jitter: 0.2
    debounce_ms: 2000
  periodic_restart_sec: 3600
  sleep_check_sec: 5
  sleep_gap_sec: 30
  network_poll_sec: 5
  local_forward: "127.0.0.1:15432:127.0.0.1:5432"
  local_forwards:
    - "127.0.0.1:16379:127.0.0.1:6379"

ssh:
  user: "ubuntu"
  host: "example.com"
  port: 22
  remote_forwards:
    - "0.0.0.0:2222:localhost:22"
    - "0.0.0.0:2223:localhost:23"
  identity_file: "~/.ssh/id_ed25519"
  options:
    - "ServerAliveInterval=30"
    - "ServerAliveCountMax=3"

logging:
  level: "info"
  path: "~/.rpa/logs/agent.log"

client_logging:
  level: "info"
  path: "~/.rpa/logs/client.log"
```

메모:
- `ssh.remote_forwards`는 중복 제거됩니다.
- `client.local_forward`/`client.local_forwards`도 동일하게 처리됩니다.
- `agent clear`는 포워드를 모두 제거하고 서비스도 내려갑니다.

## 관측성

- 로그는 JSON 라인 형식
- `last_success_unix`는 연결이 2초 이상 유지된 뒤에만 기록됨
- status/metrics 상세 스키마: `docs/OBSERVABILITY.md`

## 릴리스 (GitHub Releases)

`v*` 태그를 푸시하면 GoReleaser가 바이너리를 업로드합니다.
```sh
git tag v0.1.0
git push origin v0.1.0
```

## 개발

```sh
go test ./...
```
