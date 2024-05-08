// Copyright 2013 The imageproxy authors.
// SPDX-License-Identifier: Apache-2.0

package imageproxy

import (
	"bytes"
	"fmt"
	"github.com/disintegration/imaging"
	"github.com/jdeng/goheif"
	"github.com/muesli/smartcrop"
	"github.com/muesli/smartcrop/nfnt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rwcarlsen/goexif/exif"
	"golang.org/x/image/bmp"    // register bmp format
	"golang.org/x/image/tiff"   // register tiff format
	_ "golang.org/x/image/webp" // register webp format
	"image"
	_ "image/gif" // register gif format
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"math"
	"willnorris.com/go/gifresize"
)

// Skip Writer for exif writing
type writerSkipper struct {
	w           io.Writer
	bytesToSkip int
}

func (w *writerSkipper) Write(data []byte) (int, error) {
	if w.bytesToSkip <= 0 {
		return w.w.Write(data)
	}

	if dataLen := len(data); dataLen < w.bytesToSkip {
		w.bytesToSkip -= dataLen
		return dataLen, nil
	}

	if n, err := w.w.Write(data[w.bytesToSkip:]); err == nil {
		n += w.bytesToSkip
		w.bytesToSkip = 0
		return n, nil
	} else {
		return n, err
	}
}

func newWriterExif(w io.Writer, exif []byte) (io.Writer, error) {
	writer := &writerSkipper{w, 2}
	soi := []byte{0xff, 0xd8}
	if _, err := w.Write(soi); err != nil {
		return nil, err
	}

	if exif != nil {
		app1Marker := 0xe1
		markerlen := 2 + len(exif)
		marker := []byte{0xff, uint8(app1Marker), uint8(markerlen >> 8), uint8(markerlen & 0xff)}
		if _, err := w.Write(marker); err != nil {
			return nil, err
		}

		if _, err := w.Write(exif); err != nil {
			return nil, err
		}
	}

	return writer, nil
}

// default compression quality of resized jpegs
const defaultQuality = 95

// maximum distance into image to look for EXIF tags
const maxExifSize = 1 << 20

// resample filter used when resizing images
var resampleFilter = imaging.Lanczos

func isHEIC(img []byte) bool {
	// HEIC files start with the string "ftypheic", "ftypmif1", "ftypmsf1" or "ftypheix" at the 5th byte
	return string(img[4:12]) == "ftypheic" || string(img[4:12]) == "ftypmif1" || string(img[4:12]) == "ftypmsf1" || string(img[4:12]) == "ftypheix"
}

// Transform the provided image.  img should contain the raw bytes of an
// encoded image in one of the supported formats (gif, jpeg, or png).  The
// bytes of a similarly encoded image is returned.
func Transform(img []byte, opt Options) ([]byte, error) {
	if !opt.transform() {
		// bail if no transformation was requested
		return img, nil
	}
	var m image.Image
	var err error
	var format string
	// check if img isHEIC
	if isHEIC(img) {
		// decode image
		m, err = goheif.Decode(bytes.NewReader(img))
		if err != nil {
			log.Fatalf("Failed to parse %s: ", err)
		}
		format = "heic"
	} else {
		// decode image
		m, format, err = image.Decode(bytes.NewReader(img))
		if err != nil {
			return nil, err
		}
	}

	// apply EXIF orientation for jpeg and tiff source images. Read at most
	// up to maxExifSize looking for EXIF tags.
	if format == "jpeg" || format == "tiff" {
		r := io.LimitReader(bytes.NewReader(img), maxExifSize)
		if exifOpt := exifOrientation(r); exifOpt.transform() {
			m = transformImage(m, exifOpt)
		}
	}

	// encode webp and tiff as jpeg by default
	if format == "tiff" || format == "webp" {
		format = "jpeg"
	}

	if opt.Format != "" {
		format = opt.Format
	}

	// transform and encode image
	buf := new(bytes.Buffer)
	switch format {
	case "heic":
		m = transformImage(m, opt)
		buf := new(bytes.Buffer)
		w, _ := newWriterExif(buf, nil)
		err = jpeg.Encode(w, m, nil)
		if err != nil {
			log.Fatalf("Failed to encode to buffer: %v\n", err)
		}
		log.Printf("Converted HEIC to JPEG successfully\n")
		return buf.Bytes(), nil

	case "bmp":
		m = transformImage(m, opt)
		err = bmp.Encode(buf, m)
		if err != nil {
			return nil, err
		}
	case "gif":
		fn := func(img image.Image) image.Image {
			return transformImage(img, opt)
		}
		err = gifresize.Process(buf, bytes.NewReader(img), fn)
		if err != nil {
			return nil, err
		}
	case "jpeg":
		quality := opt.Quality
		if quality == 0 {
			quality = defaultQuality
		}

		m = transformImage(m, opt)
		err = jpeg.Encode(buf, m, &jpeg.Options{Quality: quality})
		if err != nil {
			return nil, err
		}
	case "png":
		m = transformImage(m, opt)
		err = png.Encode(buf, m)
		if err != nil {
			return nil, err
		}
	case "tiff":
		m = transformImage(m, opt)
		err = tiff.Encode(buf, m, &tiff.Options{Compression: tiff.Deflate, Predictor: true})
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported format: %v", format)
	}

	return buf.Bytes(), nil
}

// evaluateFloat interprets the option value f. If f is between 0 and 1, it is
// interpreted as a percentage of max, otherwise it is treated as an absolute
// value.  If f is less than 0, 0 is returned.
func evaluateFloat(f float64, max int) int {
	if 0 < f && f < 1 {
		return int(float64(max) * f)
	}
	if f < 0 {
		return 0
	}
	return int(f)
}

// resizeParams determines if the image needs to be resized, and if so, the
// dimensions to resize to.
func resizeParams(m image.Image, opt Options) (w, h int, resize bool) {
	// convert percentage width and height values to absolute values
	imgW := m.Bounds().Dx()
	imgH := m.Bounds().Dy()
	w = evaluateFloat(opt.Width, imgW)
	h = evaluateFloat(opt.Height, imgH)

	// never resize larger than the original image unless specifically allowed
	if !opt.ScaleUp {
		if w > imgW {
			w = imgW
		}
		if h > imgH {
			h = imgH
		}
	}

	// if requested width and height match the original, skip resizing
	if (w == imgW || w == 0) && (h == imgH || h == 0) {
		return 0, 0, false
	}

	return w, h, true
}

var smartcropAnalyzer = smartcrop.NewAnalyzer(nfnt.NewDefaultResizer())

// cropParams calculates crop rectangle parameters to keep it in image bounds
func cropParams(m image.Image, opt Options) image.Rectangle {
	if !opt.SmartCrop && opt.CropX == 0 && opt.CropY == 0 && opt.CropWidth == 0 && opt.CropHeight == 0 {
		return m.Bounds()
	}

	// width and height of image
	imgW := m.Bounds().Dx()
	imgH := m.Bounds().Dy()

	if opt.SmartCrop {
		w := evaluateFloat(opt.Width, imgW)
		h := evaluateFloat(opt.Height, imgH)
		r, err := smartcropAnalyzer.FindBestCrop(m, w, h)
		if err != nil {
			log.Printf("smartcrop error finding best crop: %v", err)
		} else {
			return r
		}
	}

	// top left coordinate of crop
	x0 := evaluateFloat(math.Abs(opt.CropX), imgW)
	if opt.CropX < 0 {
		x0 = imgW - x0 // measure from right
	}
	y0 := evaluateFloat(math.Abs(opt.CropY), imgH)
	if opt.CropY < 0 {
		y0 = imgH - y0 // measure from bottom
	}

	// width and height of crop
	w := evaluateFloat(opt.CropWidth, imgW)
	if w == 0 {
		w = imgW
	}
	h := evaluateFloat(opt.CropHeight, imgH)
	if h == 0 {
		h = imgH
	}

	// bottom right coordinate of crop
	x1 := x0 + w
	if x1 > imgW {
		x1 = imgW
	}
	y1 := y0 + h
	if y1 > imgH {
		y1 = imgH
	}

	return image.Rect(x0, y0, x1, y1)
}

// read EXIF orientation tag from r and adjust opt to orient image correctly.
func exifOrientation(r io.Reader) (opt Options) {
	// Exif Orientation Tag values
	// http://sylvana.net/jpegcrop/exif_orientation.html
	const (
		topLeftSide     = 1
		topRightSide    = 2
		bottomRightSide = 3
		bottomLeftSide  = 4
		leftSideTop     = 5
		rightSideTop    = 6
		rightSideBottom = 7
		leftSideBottom  = 8
	)

	ex, err := exif.Decode(r)
	if err != nil {
		return opt
	}
	tag, err := ex.Get(exif.Orientation)
	if err != nil {
		return opt
	}
	orient, err := tag.Int(0)
	if err != nil {
		return opt
	}

	switch orient {
	case topLeftSide:
		// do nothing
	case topRightSide:
		opt.FlipHorizontal = true
	case bottomRightSide:
		opt.Rotate = 180
	case bottomLeftSide:
		opt.FlipVertical = true
	case leftSideTop:
		opt.Rotate = 90
		opt.FlipVertical = true
	case rightSideTop:
		opt.Rotate = -90
	case rightSideBottom:
		opt.Rotate = 90
		opt.FlipHorizontal = true
	case leftSideBottom:
		opt.Rotate = 90
	}
	return opt
}

// transformImage modifies the image m based on the transformations specified
// in opt.
func transformImage(m image.Image, opt Options) image.Image {
	timer := prometheus.NewTimer(metricTransformationDuration)
	defer timer.ObserveDuration()

	// Parse crop and resize parameters before applying any transforms.
	// This is to ensure that any percentage-based values are based off the
	// size of the original image.
	rect := cropParams(m, opt)
	w, h, resize := resizeParams(m, opt)

	// crop if needed
	if !m.Bounds().Eq(rect) {
		m = imaging.Crop(m, rect)
	}
	// resize if needed
	if resize {
		if opt.Fit {
			m = imaging.Fit(m, w, h, resampleFilter)
		} else {
			if w == 0 || h == 0 {
				m = imaging.Resize(m, w, h, resampleFilter)
			} else {
				m = imaging.Thumbnail(m, w, h, resampleFilter)
			}
		}
	}

	// rotate
	rotate := float64(opt.Rotate) - math.Floor(float64(opt.Rotate)/360)*360
	switch rotate {
	case 90:
		m = imaging.Rotate90(m)
	case 180:
		m = imaging.Rotate180(m)
	case 270:
		m = imaging.Rotate270(m)
	}

	// flip
	if opt.FlipVertical {
		m = imaging.FlipV(m)
	}
	if opt.FlipHorizontal {
		m = imaging.FlipH(m)
	}

	return m
}
