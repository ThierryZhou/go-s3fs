package s3

import "fmt"

var (
	ErrInvalidBucketName error = fmt.Errorf("Invalid Bucket Name")
	ErrBucketExisted     error = fmt.Errorf("Bucket Does not Exists")
)
