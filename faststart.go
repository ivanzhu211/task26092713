// Package faststart relocates the moov box of a non-fragmented MP4
// (ISO BMFF) file to the front of the file so browsers can start
// playback before the download completes. It uses only the standard
// library and never rewrites mdat payloads.
package faststart

import "errors"

var (
	// ErrFragmented is returned when the file contains moof/mvex boxes.
	ErrFragmented = errors.New("faststart: fragmented MP4 not supported")
	// ErrTruncated is returned when a box extends past the end of file.
	ErrTruncated = errors.New("faststart: truncated file")
	// ErrBadSize is returned when a declared box size is out of range.
	ErrBadSize = errors.New("faststart: invalid box size")
	// ErrBadOffset is returned when a chunk offset points outside all
	// mdat payloads.
	ErrBadOffset = errors.New("faststart: chunk offset outside mdat payload")
	// ErrInconsistent is returned when sample tables contradict each other.
	ErrInconsistent = errors.New("faststart: inconsistent sample tables")
	// ErrNoMoov is returned when the file has no moov box.
	ErrNoMoov = errors.New("faststart: no moov box")
)
