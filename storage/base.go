package storage

import (
	"io"
	"os"
	"rsync-os/rsync"
)

type FileMetadata struct {
	Mtime int32
	Mode  os.FileMode
}

type IO interface {
	Write(objectName string, reader io.Reader, objectSize int64, metadata FileMetadata) (n int64, err error)
	//Read(fileName string, metadata FileMetadata)
	Delete(objectName string) error
	List() rsync.FileList
}
