package s3fs

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/golang/groupcache/lru"
	log "github.com/sirupsen/logrus"

	appconf "registry.code.tuya-inc.top/TuyaAiPlatform/dataset-server/pkg/config"
	"registry.code.tuya-inc.top/TuyaAiPlatform/dataset-server/pkg/storage"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	s3v2 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	_ "github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/smithy-go"
)

const (
	defaultShareLinkExpiry = time.Hour * 24 * 7 // 7 days
	defaultCacheSize       = 500000
)

type s3Client struct {
	client       *s3v2.Client
	downloader   *manager.Downloader
	uploader     *manager.Uploader
	presignCache *lru.Cache
}

type NoOpRateLimit struct{}

func (NoOpRateLimit) AddTokens(uint) error { return nil }
func (NoOpRateLimit) GetToken(context.Context, uint) (func() error, error) {
	return noOpToken, nil
}
func noOpToken() error { return nil }

type ExponentialJitterBackoff struct {
	minDelay           time.Duration
	maxBackoffAttempts int
}

func NewExponentialJitterBackoff(minDelay time.Duration, maxAttempts int) *ExponentialJitterBackoff {
	return &ExponentialJitterBackoff{minDelay, maxAttempts}
}

func (j *ExponentialJitterBackoff) BackoffDelay(attempt int, err error) (time.Duration, error) {
	minDelay := j.minDelay

	log.Printf("retryCount: %d", attempt)
	var jitter = float64(rand.Intn(120-80)+80) / 100
	retryTime := time.Duration(int(float64(int(minDelay.Nanoseconds())*int(math.Pow(3, float64(attempt)))) * jitter))

	// Cap retry time at 5 minutes to avoid too long a wait
	if retryTime > time.Duration(5*time.Minute) {
		retryTime = time.Duration(5 * time.Minute)
	}

	return retryTime, nil
}

func NewS3Client(args string) (storage.Client, error) {
	// u, err := url.Parse(o.URL)
	// if err != nil {
	// 	log.Printf("url.Parse(%s): err = %#v", o.URL, err)
	// 	return nil, err
	// }
	o := ParseOption(args)
	host := o.URL
	access_key := o.AccessKey
	secret_key := o.SecretKey
	// secure := u.Scheme == "https"

	customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		return aws.Endpoint{
			PartitionID:   "S3",
			URL:           host,
			SigningRegion: "us-east-1",
		}, nil
	})

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		// config.WithClientLogMode(aws.LogRetries|aws.LogRequest|aws.LogResponse),
		config.WithClientLogMode(aws.LogRetries),
		config.WithRetryer(func() aws.Retryer {
			return retry.AddWithMaxBackoffDelay(retry.NewStandard(func(o *retry.StandardOptions) {
				o.MaxAttempts = 20
				o.RateLimiter = NoOpRateLimit{}
				backoff := NewExponentialJitterBackoff(25*time.Millisecond, 9)
				o.Backoff = backoff
			}), 20*time.Second)
		}),
		config.WithEndpointResolverWithOptions(customResolver))
	if err != nil {
		panic(err)
	}

	client := s3v2.NewFromConfig(cfg, func(o *s3v2.Options) {
		o.UsePathStyle = true
		o.Credentials = aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(access_key, secret_key, ""))
	})

	psClient := s3v2.NewPresignClient(client)

	downloader := manager.NewDownloader(client)

	uploader := manager.NewUploader(client)

	presignCache := lru.New(defaultCacheSize)

	return &s3Client{
		client:       client,
		psClient:     psClient,
		downloader:   downloader,
		uploader:     uploader,
		o:            o,
		presignCache: presignCache,
	}, nil
}

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < len(r)/2; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func (c *s3Client) validateUser(ctx context.Context, id string) bool {
	return true
}

func (c *s3Client) CreateUser(ctx context.Context, user string) error {
	if c.validateUser(ctx, user) {
		log.Warnf("user %s already exist in minio", user)
		return fmt.Errorf("user %s already exist in minio", user)
	}

	return nil
}

func (c *s3Client) RemoveUser(ctx context.Context, user string) error {
	if c.validateUser(ctx, user) {
		log.Warnf("user %s already exist in minio", user)
		return fmt.Errorf("user %s already exist in minio", user)
	}

	return nil
}

func (c *s3Client) userDefaultSecret(user string) string {
	s := reverse(user)
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func (c *s3Client) validateBucket(ctx context.Context, name string) int {

	if len(name) <= 2 {
		return -1
	}

	_, err := c.HeadBucket(ctx, name)
	if err != nil {
		return 0
	}

	return 1
}

func (c *s3Client) IsBucketExist(ctx context.Context, name string) bool {

	input := &s3v2.HeadBucketInput{
		Bucket: aws.String(name),
	}

	result, err := c.client.HeadBucket(ctx, input)
	if err != nil {
		var nsb *types.NoSuchBucket
		if errors.As(err, &nsb) {
			log.Warn("NoSuchBucket")
		}

		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			log.Warn(apiErr.ErrorMessage())
		}

		return false
	}

	if result == nil {
		return false
	}

	return true
}

func (c *s3Client) bucketName(name string) string {
	return name
}

func (c *s3Client) policyName(user string) string {
	return user
}

func (c *s3Client) HeadBucket(ctx context.Context, bucket string) (bool, error) {

	input := &s3v2.HeadBucketInput{
		Bucket: aws.String(bucket),
	}

	_, err := c.client.HeadBucket(ctx, input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			log.Warnf("Head Bucket(%s) with Error:%s", bucket, apiErr.ErrorMessage())
		}
		return false, err
	}

	return true, nil
}

func (c *s3Client) CreateBucket(ctx context.Context, user, name string) (*storage.Bucket, error) {

	// check bucket exists
	if c.validateBucket(ctx, name) == -1 {
		return nil, storage.ErrInvalidBucketName
	} else if c.validateBucket(ctx, name) == 1 {
		return nil, storage.ErrBucketExisted
	}

	// create it if not exists
	input := &s3v2.CreateBucketInput{
		Bucket: aws.String(name),
	}

	_, err := c.client.CreateBucket(ctx, input)
	if err != nil {
		var nsb *types.BucketAlreadyExists
		if errors.As(err, &nsb) {
			log.Warn("BucketAlreadyExists")
		}

		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			log.Warn(apiErr.ErrorMessage())
		}

		return nil, err
	}

	return &storage.Bucket{
		Name: name,
	}, nil

}

func (c *s3Client) DeleteBucket(ctx context.Context, user, name string) error {
	// check bucket name
	if c.validateBucket(ctx, name) != 1 {
		return storage.ErrInvalidBucketName
	}

	// // delete bucket's shares
	// shares, err := c.listBucketShares(ctx, name)
	// if err != nil {
	// 	return err
	// }
	// for n, share := range shares {
	// 	err := c.DeleteShare(ctx, user, name, share.User)
	// 	log.Printf("%d: delete share of bucket %s from user %s, err = %#v", n, name, share.User, err)
	// }

	// // remove bucket from owner's policy
	// policyName := c.policyName(user)
	// apolicy, _ := c.adminClient.InfoCannedPolicy(ctx, policyName)
	// upolicy, _ := newBucketPolicyFromPolicy(apolicy)
	// upolicy.removeReadWriteBucket(name)
	// apolicy = upolicy.policy()

	// if err := c.adminClient.AddCannedPolicy(ctx, policyName, apolicy); err != nil {
	// 	log.Printf("madmin.AddCannedPolicy(%s, %s): err = %#v", policyName, apolicy, err)
	// 	return err
	// }

	inV2 := &s3v2.ListObjectsV2Input{
		Bucket: aws.String(name),
	}

	for {
		out, err := c.client.ListObjectsV2(ctx, inV2)
		if err != nil {
			log.Fatalf("Failed to list version objects with api ListObjectsV2: %v", err)
		}

		var wg sync.WaitGroup
		cos := make(chan error, appconf.MAX_GOROUTES)
		for _, item := range out.Contents {
			wg.Add(1)

			go func() {
				cos <- c.DeleteObject(ctx, user, name, aws.ToString(item.Key))
			}()

			go func() {
				wg.Wait()
				close(cos)
			}()

			for ret := range cos {
				if ret != nil {
					log.Warnf("Failed to Delete Object: %v", err)
					return err
				}
			}

			// err = c.DeleteObject(ctx, user, name, aws.ToString(item.Key))
			// if err != nil {
			// 	log.Fatalf("Failed to Delete Object: %v", err)
			// 	return err
			// }
		}

		wg.Wait()

		if out.IsTruncated {
			inV2.ContinuationToken = out.ContinuationToken
		} else {
			break
		}
	}

	// delete bucket
	input := &s3v2.DeleteBucketInput{
		Bucket: aws.String(name),
	}

	_, err := c.client.DeleteBucket(ctx, input)
	if err != nil {

		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			log.Warn("client.RemoveBucket(%s): err = %s", name, apiErr.ErrorMessage())
		}
		return err
	}

	return nil
}

func (c *s3Client) listBucketShares(ctx context.Context, name string) ([]*storage.Share, error) {
	// apolicies, err := c.adminClient.ListCannedPolicies(ctx)
	// if err != nil {
	// 	log.Printf("madmin.ListCannedPolicies(): err = %#v", err)
	// 	return nil, err
	// }

	shares := []*storage.Share{}
	// for k, v := range apolicies {
	// 	if strings.HasSuffix(k, c.o.UserIDSuffix) {
	// 		// upolicy, _ := newBucketPolicyFromPolicy(v)
	// 		// if upolicy.findReadOnlyBucket(name) {
	// 		// 	shares = append(shares, &storage.Share{
	// 		// 		User: k,
	// 		// 	})
	// 		// }
	// 	}
	// }

	return shares, nil
}

func (c *s3Client) CreateShare(ctx context.Context, user, name, targetUser string) error {

	// check bucket name
	if c.validateBucket(ctx, name) != 1 {
		return storage.ErrInvalidBucketName
	}

	// check owner
	{
		if !c.validateUser(ctx, user) {
			return storage.ErrInvalidParams
		}

		// get owner's policy

		// check owner privilege

		// valid owneer's policy
	}

	// check touser
	{
		if !c.validateUser(ctx, targetUser) {
			return storage.ErrInvalidParams
		}

		// get touser's policy
	}

	return nil
}

func (c *s3Client) DeleteShare(ctx context.Context, user, name, targetUser string) error {
	if !c.validateUser(ctx, user) {
		return storage.ErrInvalidParams
	}

	if !c.validateUser(ctx, targetUser) {
		return storage.ErrInvalidParams
	}

	if c.validateBucket(ctx, name) != 1 {
		return storage.ErrInvalidBucketName
	}

	return nil
}

func (c *s3Client) Account(ctx context.Context, user, token string) (*storage.Account, error) {
	if !c.validateUser(ctx, user) {
		return nil, storage.ErrInvalidParams
	}

	u, err := url.Parse(c.o.ExternalURL)
	if err != nil {
		log.Printf("url.Parse(%s): err = %#v", c.o.ExternalURL, err)
		return nil, storage.ErrInvalidParams
	}
	minioURL := fmt.Sprintf("%s://%s", u.Scheme, u.Host)

	return &storage.Account{
		URL:      minioURL,
		User:     user,
		Password: c.userDefaultSecret(user),
		Description: fmt.Sprintf("mc alias set myminio %s %s %s; mc ls myminio",
			minioURL,
			user,
			c.userDefaultSecret(user),
		),
	}, nil
}

const (
	loginS3Html = `
<html>
<head></head>
<body>
<script>window.localStorage.setItem('token','%s')</script>
<script>window.location.href='%s'</script>
</body>
</html>
`
)

func (c *s3Client) Redirect(ctx context.Context, user, token, dir string, w http.ResponseWriter) error {
	if !c.validateUser(ctx, user) {
		http.Error(w, storage.ErrInvalidParams.Error(), http.StatusBadRequest)
		return nil
	}

	log.Printf("==> user = %s, token = %s, dir = %s", user, token, dir)

	return nil
}

func (c *s3Client) Volume(ctx context.Context, userID, bucketPath, customPath string) (string, error) {
	if !c.validateUser(ctx, userID) {
		return "", storage.ErrInvalidParams
	}

	tpl, err := template.ParseFiles(c.o.VolumeTemplate)
	if err != nil {
		log.Printf("ParseFiles(%s): err = %#v", c.o.VolumeTemplate, err)
		return "", err
	}

	bucketName := strings.Split(bucketPath, "/")[0]
	readOnly := "true"

	log.Printf("bucketPath(%s), customPath(%s) -> bucketName(%s): readOnly = %s",
		bucketPath, customPath, bucketName, readOnly)

	var buf bytes.Buffer

	data := map[string]string{
		"Path":     bucketPath, // path.Join(bucketPath, customPath),
		"ReadOnly": readOnly,
	}
	if err := tpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func (c *s3Client) PutFile(ctx context.Context, userID, bucket, path, file string) (*storage.Object, error) {
	// check bucket exists
	if c.validateBucket(ctx, bucket) != 1 {
		return nil, storage.ErrInvalidBucketName
	}

	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("can't open local file")
	}
	defer f.Close()

	// analyze file path
	cpath := filepath.Clean(fmt.Sprintf("./%s", path))
	dir, file_name := filepath.Split(cpath)

	// create it if not exists
	input := &s3v2.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(cpath),
		Body:   f,
	}

	_, err = c.client.PutObject(ctx, input)

	if err != nil {
		var nsb *types.NoSuchBucket
		var nsk *types.NoSuchKey
		switch {
		case errors.As(err, &nsb):
			log.Warnf("Put File(%s) into Bucket(%s) with AWS S3 Error: %s", bucket, path, *nsb.Message)
		case errors.As(err, &nsb):
			log.Warnf("Put File(%s) into Bucket(%s) with AWS S3 Error: %s", bucket, path, *nsk.Message)
		default:
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				log.Warnf("Put File(%s) into Bucket(%s) with Unknown Error:%s", path, bucket, apiErr.ErrorMessage())
			}
		}

		return nil, err
	}

	return &storage.Object{
		Bucket:   bucket,
		FileName: file_name,
		Dir:      dir,
	}, nil
}

func (c *s3Client) ShareObject(ctx context.Context, userID, name, objectPath, expiry string) (*storage.ShareLink, error) {
	if !c.validateUser(ctx, userID) {
		return nil, storage.ErrInvalidParams
	}

	return nil, nil
}

func (c *s3Client) PutObject(ctx context.Context, userID, bucket, path string, data []byte) (*storage.Object, error) {
	// check bucket exists
	if c.validateBucket(ctx, bucket) != 1 {
		return nil, storage.ErrInvalidBucketName
	}

	// analyze file path
	cpath := filepath.Clean(fmt.Sprintf("./%s", path))
	dir, file_name := filepath.Split(cpath)

	// create it if not exists
	input := &s3v2.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(cpath),
		Body:   bytes.NewReader(data),
	}

	_, err := c.client.PutObject(ctx, input)
	if err != nil {
		var nsb *types.NoSuchBucket
		var nsk *types.NoSuchKey
		switch {
		case errors.As(err, &nsb):
			log.Warnf("Put Object(%s) into Bucket(%s) with AWS S3 Error: %s", bucket, path, *nsb.Message)
		case errors.As(err, &nsb):
			log.Warnf("Put Object(%s) into Bucket(%s) with AWS S3 Error: %s", bucket, path, *nsk.Message)
		default:
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				log.Warnf("Put Object(%s) into Bucket(%s) with Unknown Error:%s", path, bucket, apiErr.ErrorMessage())
			}
		}

		return nil, err
	}

	return &storage.Object{
		Bucket:   bucket,
		FileName: file_name,
		Dir:      dir,
		Data:     data,
	}, nil
}

func (c *s3Client) GetObject(ctx context.Context, userID, bucket, path string) (*storage.Object, error) {
	// check bucket exists
	if c.validateBucket(ctx, bucket) != 1 {
		return nil, storage.ErrInvalidBucketName
	}

	cpath := filepath.Clean(fmt.Sprintf("./%s", path))

	dir, file_name := filepath.Split(cpath)

	contentLength, err := c.HeadObject(ctx, userID, bucket, path)
	if err != nil {
		return nil, err
	}

	data := make([]byte, contentLength)
	buf := manager.NewWriteAtBuffer(data)

	// create it if not exists
	input := &s3v2.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(cpath),
	}

	numBytes, err := c.downloader.Download(context.TODO(), buf, input)
	if err != nil {
		var nsb *types.NoSuchBucket
		var nsk *types.NoSuchKey
		switch {
		case errors.As(err, &nsb):
			log.Warnf("Get Object(%s) From Bucket(%s) with AWS S3 Error: %s", path, bucket, nsb.Message)
		case errors.As(err, &nsk):
			log.Warnf("Get Object(%s) From Bucket(%s) with AWS S3 Error: %s", path, bucket, nsk.Message)
			return &storage.Object{
				Bucket:   bucket,
				FileName: file_name,
				Dir:      dir,
				Data:     []byte("{}"),
			}, nil
		default:
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				log.Warnf("Get Object(%s) From Bucket(%s) with Unknown Error:%s", path, bucket, apiErr.ErrorMessage())
			}
		}

		return nil, err
	}

	if numBytes != contentLength {
		return nil, fmt.Errorf("Unknown Error In Ceph RGW")
	}

	return &storage.Object{
		Bucket:   bucket,
		FileName: file_name,
		Dir:      dir,
		Data:     data,
	}, nil
}

func (c *s3Client) DeleteObject(ctx context.Context, user, bucket, path string) error {
	// check bucket exists
	if c.validateBucket(ctx, bucket) != 1 {
		return storage.ErrInvalidBucketName
	}

	// clean root path to relative path
	cpath := filepath.Clean(fmt.Sprintf("./%s", path))

	// create it if not exists
	input := &s3v2.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(cpath),
	}

	_, err := c.client.DeleteObject(ctx, input)
	if err != nil {
		var nsb *types.NoSuchBucket
		var nsk *types.NoSuchKey
		switch {
		case errors.As(err, &nsb):
			log.Warnf("Delete Object(%s) from Bucket(%s) with AWS S3 Error: %s", path, bucket, nsb.Message)
		case errors.As(err, &nsk):
			log.Warnf("Delete Object(%s) from Bucket(%s) with AWS S3 Error: %s", path, bucket, nsk.Message)
		default:
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				log.Warnf("Delete Object(%s) from Bucket(%s) with Unknown Error:%s", path, bucket, apiErr.ErrorMessage())
			}
		}

		return err
	}

	return nil
}

func (c *s3Client) ListObject(ctx context.Context, userID, bucket, path string) ([]storage.Object, error) {
	// check bucket exists
	if c.validateBucket(ctx, bucket) != 1 {
		return nil, storage.ErrInvalidBucketName
	}

	// clean root path to relative path
	cpath := filepath.Clean(fmt.Sprintf("./%s", path))

	// create it if not exists
	input := &s3v2.ListObjectsInput{
		Bucket: aws.String(bucket),
		Prefix: aws.String(cpath),
	}

	_, err := c.client.ListObjects(ctx, input)
	if err != nil {
		var nsb *types.NoSuchBucket
		var nsk *types.NoSuchKey
		switch {
		case errors.As(err, &nsb):
			log.Warnf("List Objects(%s) from Bucket(%s) with AWS S3 Error: %s", path, bucket, nsb.Message)
		case errors.As(err, &nsk):
			log.Warnf("List Objects(%s) from Bucket(%s) with AWS S3 Error: %s", path, bucket, nsk.Message)
		default:
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				log.Warnf("List Objects(%s) from Bucket(%s) with Unknown Error:", apiErr.ErrorMessage())
			}
		}

		return nil, err
	}

	var list []storage.Object

	return list, nil
}

func (c *s3Client) HeadObject(ctx context.Context, userID, bucket, path string) (int64, error) {
	// check bucket exists
	if c.validateBucket(ctx, bucket) != 1 {
		return 0, nil
	}

	// clean root path to relative path
	cpath := filepath.Clean(fmt.Sprintf("./%s", path))

	// create it if not exists
	input := &s3v2.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(cpath),
	}

	gotOutput, err := c.client.HeadObject(ctx, input)
	if err != nil {
		var nsb *types.NoSuchBucket
		var nsk *types.NoSuchKey
		switch {
		case errors.As(err, &nsb):
			log.Warnf("Head Object(%s) from Bucket(%s) with AWS S3 Error: %s", path, bucket, nsb.Message)
		case errors.As(err, &nsk):
			log.Warnf("Head Object(%s) from Bucket(%s) with AWS S3 Error: %s", path, bucket, nsk.Message)
		default:
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				log.Warnf("Head Object(%s) from Bucket(%s) with Unknown Error:%s", path, bucket, apiErr.ErrorMessage())
			}
		}
		return 0, err
	}

	return gotOutput.ContentLength, nil
}

func (c *s3Client) UploadObject(ctx context.Context, userID, bucket, path string, file io.Reader) (*storage.Object, error) {
	// check bucket exists
	if c.validateBucket(ctx, bucket) != 1 {
		return nil, storage.ErrInvalidBucketName
	}

	// analyze file path
	cpath := filepath.Clean(fmt.Sprintf("./%s", path))
	dir, file_name := filepath.Split(cpath)

	// create it if not exists
	input := &s3v2.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(cpath),
		Body:   file,
	}

	_, err := c.uploader.Upload(ctx, input)
	if err != nil {
		var multierr manager.MultiUploadFailure
		if errors.As(err, &multierr) {
			log.Warnf("Upload Object(%s) in Bucket(%s) failure UploadID=%s, %s\n", bucket, path, multierr.UploadID(), multierr.Error())
		} else {
			log.Warnf("Upload Object(%s) in Bucket(%s) failure, %s\n", bucket, path, err.Error())
		}
		// if err != nil {
		// 	var nsb *types.NoSuchBucket
		// 	var nsk *types.NoSuchKey
		// 	switch {
		// 	case errors.As(err, &nsb):
		// 		log.Warnf("Upload Object(%s) in Bucket(%s) with AWS S3 Error: %s", bucket, path, *nsb.Message)
		// 	case errors.As(err, &nsb):
		// 		log.Warnf("Upload Object(%s) in Bucket(%s) with AWS S3 Error: %s", path, bucket, nsk.Message)
		// 	default:
		// 		var apiErr smithy.APIError
		// 		if errors.As(err, &apiErr) {
		// 			log.Warnf("Upload Object(%s) in Bucket(%s) with Unknown Error:%s", path, bucket, apiErr.ErrorMessage())
		// 		}
		// 	}

		return nil, err
	}

	return &storage.Object{
		Bucket:   bucket,
		FileName: file_name,
		Dir:      dir,
	}, nil
}

func (c *s3Client) PresignObject(ctx context.Context, userID, bucket, path string) (string, error) {
	// check bucket exists
	if c.validateBucket(ctx, bucket) != 1 {
		return "", storage.ErrInvalidBucketName
	}

	// clean root path to relative path
	cpath := filepath.Clean(fmt.Sprintf("./%s", path))

	var downloadUrl string
	// key := fmt.Sprintf("%s/%s", bucket, cpath)
	// data, ok := c.presignCache.Get(key)
	// if !ok {

	// create it if not exists
	input := &s3v2.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(cpath),
	}

	resp, err := c.psClient.PresignGetObject(ctx, input)
	if err != nil {
		var nsb *types.NoSuchBucket
		var nsk *types.NoSuchKey
		switch {
		case errors.As(err, &nsb):
			log.Warnf("Presign Object(%s) in Bucket(%s) with AWS S3 Error: %s", bucket, path, *nsb.Message)
		case errors.As(err, &nsb):
			log.Warnf("Presign Object(%s) in Bucket(%s) with AWS S3 Error: %s", path, bucket, nsk.Message)
		default:
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				log.Warnf("Presign Object(%s) in Bucket(%s) with Unknown Error:%s", path, bucket, apiErr.ErrorMessage())
			}
		}

		return "", err
	}
	downloadUrl = resp.URL
	// } else {
	// 	downloadUrl = data.(string)
	// }

	return downloadUrl, nil
}
