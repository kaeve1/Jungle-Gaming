package sqsclient

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"net/http"
	"time"
)

// Explicit wallet-service credentials prevent ambient administrative credentials
// from overriding the runtime identity. The caller supplies a timeout appropriate to health or long polling.
func New(ctx context.Context, endpoint, region, file string, timeout time.Duration) (*sqs.Client, func(), error) {
	if timeout <= 0 {
		return nil, nil, errors.New("invalid SQS timeout")
	}
	shared, err := awsconfig.LoadSharedConfigProfile(ctx, "wallet-service", func(o *awsconfig.LoadSharedConfigOptions) {
		o.CredentialsFiles = []string{file}
		o.ConfigFiles = []string{}
	})
	if err != nil || !shared.Credentials.HasKeys() {
		return nil, nil, errors.New("wallet-service SQS credentials unavailable")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := sqs.New(sqs.Options{Region: region, BaseEndpoint: aws.String(endpoint), Credentials: credentials.NewStaticCredentialsProvider(shared.Credentials.AccessKeyID, shared.Credentials.SecretAccessKey, ""), HTTPClient: &http.Client{Transport: transport, Timeout: timeout}, RetryMaxAttempts: 1})
	return client, transport.CloseIdleConnections, nil
}
