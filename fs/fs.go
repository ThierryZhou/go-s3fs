// Package fs is a generic file system interface for rclone object storage systems
package fs

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"io"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ThierryZhou/go-s3fs/fs/config/configmap"
	"github.com/ThierryZhou/go-s3fs/fs/fspath"
)

// Constants
const (
	// ModTimeNotSupported is a very large precision value to show
	// mod time isn't supported on this Fs
	ModTimeNotSupported = 100 * 365 * 24 * time.Hour
	// MaxLevel is a sentinel representing an infinite depth for listings
	MaxLevel = math.MaxInt32
)

// Globals
var (
	// ErrorNotFoundInConfigFile is returned by NewFs if not found in config file
	ErrorNotFoundInConfigFile        = errors.New("didn't find section in config file")
	ErrorCantPurge                   = errors.New("can't purge directory")
	ErrorCantCopy                    = errors.New("can't copy object - incompatible remotes")
	ErrorCantMove                    = errors.New("can't move object - incompatible remotes")
	ErrorCantDirMove                 = errors.New("can't move directory - incompatible remotes")
	ErrorCantUploadEmptyFiles        = errors.New("can't upload empty files to this remote")
	ErrorDirExists                   = errors.New("can't copy directory - destination already exists")
	ErrorCantSetModTime              = errors.New("can't set modified time")
	ErrorCantSetModTimeWithoutDelete = errors.New("can't set modified time without deleting existing object")
	ErrorDirNotFound                 = errors.New("directory not found")
	ErrorObjectNotFound              = errors.New("object not found")
	ErrorLevelNotSupported           = errors.New("level value not supported")
	ErrorListAborted                 = errors.New("list aborted")
	ErrorListBucketRequired          = errors.New("bucket or container name is needed in remote")
	ErrorIsFile                      = errors.New("is a file not a directory")
	ErrorIsDir                       = errors.New("is a directory not a file")
	ErrorNotAFile                    = errors.New("is not a regular file")
	ErrorNotDeleting                 = errors.New("not deleting files as there were IO errors")
	ErrorNotDeletingDirs             = errors.New("not deleting directories as there were IO errors")
	ErrorOverlapping                 = errors.New("can't sync or move files on overlapping remotes (try excluding the destination with a filter rule)")
	ErrorDirectoryNotEmpty           = errors.New("directory not empty")
	ErrorImmutableModified           = errors.New("immutable file modified")
	ErrorPermissionDenied            = errors.New("permission denied")
	ErrorCantShareDirectories        = errors.New("this backend can't share directories with link")
	ErrorNotImplemented              = errors.New("optional feature not implemented")
	ErrorCommandNotFound             = errors.New("command not found")
	ErrorFileNameTooLong             = errors.New("file name too long")
)

// CheckClose is a utility function used to check the return from
// Close in a defer statement.
func CheckClose(c io.Closer, err *error) {
	cerr := c.Close()
	if *err == nil {
		*err = cerr
	}
}

// FileExists returns true if a file remote exists.
// If remote is a directory, FileExists returns false.
func FileExists(ctx context.Context, fs Fs, remote string) (bool, error) {
	_, err := fs.NewObject(ctx, remote)
	if err != nil {
		if err == ErrorObjectNotFound || err == ErrorNotAFile || err == ErrorPermissionDenied {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetModifyWindow calculates the maximum modify window between the given Fses
// and the Config.ModifyWindow parameter.
func GetModifyWindow(ctx context.Context, fss ...Info) time.Duration {
	window := GetConfig(ctx).ModifyWindow
	for _, f := range fss {
		if f != nil {
			precision := f.Precision()
			if precision == ModTimeNotSupported {
				return ModTimeNotSupported
			}
			if precision > window {
				window = precision
			}
		}
	}
	return window
}

// NewFs makes a new Fs object from the path
//
// The path is of the form remote:path
//
// Remotes are looked up in the config file.  If the remote isn't
// found then NotFoundInConfigFile will be returned.
//
// On Windows avoid single character remote names as they can be mixed
// up with drive letters.
func NewFs(ctx context.Context, path string) (Fs, error) {
	Debugf(nil, "Creating backend with remote %q", path)
	if ConfigFileHasSection(path) {
		Logf(nil, "%q refers to a local folder, use %q to refer to your remote or %q to hide this warning", path, path+":", "./"+path)
	}
	fsInfo, configName, fsPath, config, err := ConfigFs(path)
	if err != nil {
		return nil, err
	}
	overridden := fsInfo.Options.Overridden(config)
	if len(overridden) > 0 {
		extraConfig := overridden.String()
		//Debugf(nil, "detected overridden config %q", extraConfig)
		md5sumBinary := md5.Sum([]byte(extraConfig))
		suffix := base64.RawURLEncoding.EncodeToString(md5sumBinary[:])
		// 5 characters length is 5*6 = 30 bits of base64
		const maxLength = 5
		if len(suffix) > maxLength {
			suffix = suffix[:maxLength]
		}
		suffix = "{" + suffix + "}"
		Debugf(configName, "detected overridden config - adding %q suffix to name", suffix)
		// Add the suffix to the config name
		//
		// These need to work as filesystem names as the VFS cache will use them
		configName += suffix
	}
	f, err := fsInfo.NewFs(ctx, configName, fsPath, config)
	if f != nil && (err == nil || err == ErrorIsFile) {
		addReverse(f, fsInfo)
	}
	return f, err
}

// ConfigFs makes the config for calling NewFs with.
//
// It parses the path which is of the form remote:path
//
// Remotes are looked up in the config file.  If the remote isn't
// found then NotFoundInConfigFile will be returned.
func ConfigFs(path string) (fsInfo *RegInfo, configName, fsPath string, config *configmap.Map, err error) {
	// Parse the remote path
	fsInfo, configName, fsPath, connectionStringConfig, err := ParseRemote(path)
	if err != nil {
		return
	}
	config = ConfigMap(fsInfo, configName, connectionStringConfig)
	return
}

// ParseRemote deconstructs a path into configName, fsPath, looking up
// the fsName in the config file (returning NotFoundInConfigFile if not found)
func ParseRemote(path string) (fsInfo *RegInfo, configName, fsPath string, connectionStringConfig configmap.Simple, err error) {
	parsed, err := fspath.Parse(path)
	if err != nil {
		return nil, "", "", nil, err
	}
	configName, fsPath = parsed.Name, parsed.Path
	var fsName string
	var ok bool
	if configName != "" {
		if strings.HasPrefix(configName, ":") {
			fsName = configName[1:]
		} else {
			m := ConfigMap(nil, configName, parsed.Config)
			fsName, ok = m.Get("type")
			if !ok {
				return nil, "", "", nil, ErrorNotFoundInConfigFile
			}
		}
	} else {
		fsName = "local"
		configName = "local"
	}
	fsInfo, err = Find(fsName)
	return fsInfo, configName, fsPath, parsed.Config, err
}

// ConfigString returns a canonical version of the config string used
// to configure the Fs as passed to fs.NewFs
func ConfigString(f Fs) string {
	name := f.Name()
	root := f.Root()
	if name == "local" && f.Features().IsLocal {
		return root
	}
	return name + ":" + root
}

// TemporaryLocalFs creates a local FS in the OS's temporary directory.
//
// No cleanup is performed, the caller must call Purge on the Fs themselves.
func TemporaryLocalFs(ctx context.Context) (Fs, error) {
	path, err := ioutil.TempDir("", "rclone-spool")
	if err == nil {
		err = os.Remove(path)
	}
	if err != nil {
		return nil, err
	}
	path = filepath.ToSlash(path)
	return NewFs(ctx, path)
}
