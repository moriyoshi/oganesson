package next

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/moriyoshi/oganesson/runtime/imageopt"
)

// This file ports next-image-loader (next/dist/build/webpack/loaders/
// next-image-loader): a static image import becomes
// {src, height, width, blurDataURL, blurWidth, blurHeight}, and the file is
// emitted under static/media/. The size and blur placeholder come from
// runtime/imageopt instead of sharp.

// nextImageLoaderRe is webpack-config's nextImageLoaderRegex.
var nextImageLoaderRe = regexp.MustCompile(`(?i)\.(png|jpg|jpeg|gif|webp|avif|ico|bmp|svg)$`)

const (
	blurImgSize = 8  // next-image-loader/blur.js BLUR_IMG_SIZE
	blurQuality = 70 // BLUR_QUALITY
)

var validBlurExt = []string{"jpeg", "png", "webp", "avif"}

// staticImageSource returns the module for an image import, or ok=false when
// the file is not an image or images.disableStaticImages is set.
func (b *builder) staticImageSource(file string) (code string, ok bool, err error) {
	if !nextImageLoaderRe.MatchString(file) {
		return "", false, nil
	}
	if images, _ := b.config["images"].(map[string]any); images["disableStaticImages"] == true {
		return "", false, nil
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return "", true, err
	}
	sum := sha256.Sum256(content)
	base := filepath.Base(file)
	ext := filepath.Ext(base)
	name := fmt.Sprintf("static/media/%s.%s%s", strings.TrimSuffix(base, ext), hex.EncodeToString(sum[:])[:8], ext)
	src := b.assetPrefix() + "/_next/" + name
	extension := strings.ToLower(strings.TrimPrefix(ext, "."))
	if extension == "jpg" {
		extension = "jpeg"
	}
	w, h, err := imageopt.Size(content)
	if err != nil {
		return "", true, fmt.Errorf("next: %s: InvalidImageFormatError: %w", file, err)
	}
	data := map[string]any{"src": src, "height": h, "width": w}
	if slices.Contains(validBlurExt, extension) && !imageopt.IsAnimated(content) {
		var bw, bh int
		if w >= h {
			bw, bh = blurImgSize, max(int(math.Round(float64(h)/float64(w)*blurImgSize)), 1)
		} else {
			bw, bh = max(int(math.Round(float64(w)/float64(h)*blurImgSize)), 1), blurImgSize
		}
		// Next encodes the placeholder in the source format; Oganesson cannot
		// encode AVIF, so that one has none (sharp failing would throw instead).
		if blur, err := imageopt.Optimize(content, "image/"+extension, blurQuality, bw, bh); err == nil {
			data["blurDataURL"] = "data:image/" + extension + ";base64," + base64.StdEncoding.EncodeToString(blur)
		}
		data["blurWidth"], data["blurHeight"] = bw, bh
	} else {
		data["blurWidth"], data["blurHeight"] = 0, 0
	}
	b.mu.Lock()
	if b.media == nil {
		b.media = map[string][]byte{}
	}
	b.media[name] = content
	b.mu.Unlock()
	// JSON key order as the loader's JSON.stringify.
	keys := []string{"src", "height", "width", "blurDataURL", "blurWidth", "blurHeight"}
	var fields []string
	for _, k := range keys {
		if v, ok := data[k]; ok {
			fields = append(fields, jsonStr(k)+":"+mustJSON(v))
		}
	}
	return "export default {" + strings.Join(fields, ",") + "};", true, nil
}
