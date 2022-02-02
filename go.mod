module github.com/weshayutin/ci-search

go 1.13

require (
	cloud.google.com/go/storage v1.12.0
	github.com/docker/go-units v0.4.0
	github.com/go-bindata/go-bindata v3.1.2+incompatible
	github.com/golang/protobuf v1.4.2
	github.com/google/go-cmp v0.5.4 // indirect
	github.com/gorilla/mux v1.7.3
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/jmoiron/sqlx v1.3.1
	github.com/kr/pretty v0.2.1 // indirect
	github.com/pkg/profile v1.5.0
	github.com/prometheus/client_golang v0.9.3
	github.com/spf13/cobra v1.1.3
	github.com/stretchr/testify v1.7.0 // indirect
	github.com/xlab/handysort v0.0.0-20150421192137-fb3537ed64a1 // indirect
	golang.org/x/time v0.0.0-20191024005414-555d28b269f0
	golang.org/x/tools v0.1.8 // indirect
	gonum.org/v1/plot v0.8.1
	google.golang.org/api v0.32.0
	k8s.io/apimachinery v0.18.0
	k8s.io/client-go v0.18.0
	k8s.io/klog v1.0.0
	k8s.io/utils v0.0.0-20200324210504-a9aa75ae1b89
	modernc.org/sqlite v1.10.0
	vbom.ml/util v0.0.0-20180919145318-efcd4e0f9787
)

replace k8s.io/client-go => k8s.io/client-go v0.17.4

//	github.com/weshayutin/ci-search v0.0.0-20220120220128-8449c154871a
