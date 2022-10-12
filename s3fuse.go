// Sync files and directories to and from local and remote object stores
package main

import (
	_ "github.com/ThierryZhou/go-s3fs/cmd" // import all commands
)

func main() {
	cmd.Main()
}
