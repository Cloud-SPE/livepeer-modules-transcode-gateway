module github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway

go 1.25.7

require (
	github.com/Cloud-SPE/livepeer-network-modules/livepeer-network-protocol/proto-go v0.0.0-00010101000000-000000000000
	github.com/Cloud-SPE/livepeer-network-modules/proto-contracts v0.0.0-00010101000000-000000000000
	github.com/aws/aws-sdk-go-v2 v1.36.0
	github.com/aws/aws-sdk-go-v2/credentials v1.17.57
	github.com/aws/aws-sdk-go-v2/service/s3 v1.71.0
	github.com/caarlos0/env/v11 v11.2.2
	github.com/danielgtaylor/huma/v2 v2.27.0
	github.com/go-chi/chi/v5 v5.1.0
	github.com/golang-migrate/migrate/v4 v4.18.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.7.1
	github.com/prometheus/client_golang v1.20.5
	github.com/resend/resend-go/v2 v2.13.0
	google.golang.org/grpc v1.81.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.6.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.3.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.6.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.3.25 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.12.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.4.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.12.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.18.6 // indirect
	github.com/aws/smithy-go v1.22.2 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.17.10 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/mitchellh/mapstructure v1.4.1 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.55.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/yutopp/go-amf0 v0.1.0 // indirect
	github.com/yutopp/go-flv v0.3.1 // indirect
	github.com/yutopp/go-rtmp v0.0.7 // indirect
	go.uber.org/atomic v1.7.0 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/net v0.51.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260226221140-a57be14db171 // indirect
)

replace (
	github.com/Cloud-SPE/livepeer-network-modules/livepeer-network-protocol/proto-go => ./gen/proto-go
	github.com/Cloud-SPE/livepeer-network-modules/proto-contracts => ./gen/proto-contracts
)
