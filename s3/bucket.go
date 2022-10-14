package s3

type Bucket struct {
	Name string
}

type Object struct {
	Bucket string
	Name   string
	Prefix string
	Size   uint32
	Data   uint64
}
