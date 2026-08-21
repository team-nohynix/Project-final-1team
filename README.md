# Project-final-1team

본 프로젝트의 백앤드는 Go 언어로 개발 하였습니다. <br>
## Go 개발 환경 설정
1. https://go.dev/dl/ 에서 본인의 환경에 맞는 Go 설치 파일 다운로드<br>
2. 기본 설치 설정으로 Go를 설치해주세요<br>
  2-1. 설치 확인: CMD나 PowerShell에서 ```go version``` 입력하기
3. 코드 편접기로는 VSC를 사용합니다.
  3-1. VSC 익스텐션에서 Go (by Go Team at Google)을 설치해주세요
4. VSC에서 Ctrl + Shift + P를 눌러 명령어 팔레트를 열어주세요
5. 검색창에 Go: Install/Update Tools를 찾아 클릭합니다.
  5-1. 체크박스 목록이 나오면 전체를 선택한 후 OK
  5-2. output 콘솔에 다음과 같이 출력되면 설치가 완료된것 입니다.<br>
  ```
  [info] Tools environment: GOPATH=C:\Users\rlaeh\go, GOTOOLCHAIN=auto
  [info] Installing 1 tool at C:\Users\rlaeh\go\bin
  [info]   gopls
  [info] Installing golang.org/x/tools/gopls@latest (C:\Users\rlaeh\go\bin\gopls.exe) SUCCEEDED
  [info] All tools successfully installed. You are ready to Go. :)
  [info] Try to start language server - activation (enabled: true)
  [info] Running language server gopls(v0.23.0/go1.26.5)
  ```
6. VSC에서 backend 폴더를 터미널에서 연 뒤에 다음 명령어를 입력합니다
  ```
  go mod init backend
  ```
7. 환경 설정이 끝났습니다. ```go run main.go``` 를 통해 main.go를 실행합니다.
8. 의존성은 go.mod와 go.sum 파일에 정보가 기록됩니다. ```go mod download``` 명령어로 필요한 의존성을 모두 다운로드 합니다.


## 백엔드 실행 방법
 필요 의존성 : kafka <br>
 1. infra/dev-kafka 디렉터리를 터미널에서 열어줍니다.
 2. ```docker-compose up -d```명령어로 kafka를 실행해줍니다.
 3. backend 디렉터리를 터미널에서 열어줍니다.
 4. ```go run main.go``` 명령어로 백엔드를 실행합니다. 
