package imgfind

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// One threshold does not read every label. The stroke pass keeps
// the button labels, but it breaks the thin stems of tall display type,
// so a title is misread, and so is a short word whose letters are
// thin. Dilating once joins a stem: the title pass then reads the
// title, and a slightly lighter dilated pass reads the short word.
// Dilation smears some button words, so those stay with the stroke pass.
//
// A tall word trusts the title pass. A short word trusts the plain
// reading with the highest confidence, and falls back to the stroke pass
// when that is the only plain reading: tesseract can report confidence 0
// for a correct button word, and the other passes punctuate it.
// Measured on a sample HUD texture that is not in the repository.
// Why: doc/imgfind.md#how-text-is-read
const (
	passStroke = iota
	passClosed
	passTitle

	// titleHeight is the shortest word that is display type rather than
	// a button label. Titles in the sample HUD texture, which is not in
	// the repository, are about 50px and labels about 25.
	titleHeight = 40
)

type ocrPass struct {
	id  int
	thr int
	dil int
}

var ocrPasses = []ocrPass{
	{passStroke, 140, 0},
	{passClosed, 125, 1},
	{passTitle, 115, 1},
}

// span is a recognized word, or several words on one line.
type span struct {
	text string
	x, y int
	w, h int
}

type cand struct {
	text       string
	conf       float64
	x, y, w, h int
	pass       int
}

func readText(img image.Image) ([]span, error) {
	bin, err := exec.LookPath("tesseract")
	if err != nil {
		return nil, fmt.Errorf("imgfind: tesseract not on PATH: %w", err)
	}
	w, h, lum := luminance(img)

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		cands []cand
		first error
	)
	for _, p := range ocrPasses {
		wg.Add(1)
		go func(p ocrPass) {
			defer wg.Done()
			got, err := runPass(bin, lum, w, h, p)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && first == nil {
				first = err
			}
			cands = append(cands, got...)
		}(p)
	}
	wg.Wait()
	if first != nil {
		return nil, first
	}
	words := chooseWords(cands)
	return append(words, phrases(words)...), nil
}

func runPass(bin string, lum []byte, w, h int, p ocrPass) ([]cand, error) {
	mask := make([]byte, len(lum))
	for i, v := range lum {
		if int(v) < p.thr {
			mask[i] = 1
		}
	}
	for d := 0; d < p.dil; d++ {
		mask = dilate(mask, w, h)
	}
	g := image.NewGray(image.Rect(0, 0, w, h))
	for i, v := range mask {
		if v == 1 {
			g.Pix[i] = 0
		} else {
			g.Pix[i] = 255
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, g); err != nil {
		return nil, fmt.Errorf("imgfind: encode: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "stdin", "stdout", "--psm", "11", "tsv")
	cmd.Stdin = &buf
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, fmt.Errorf("imgfind: tesseract: %w", err)
		}
		return nil, fmt.Errorf("imgfind: tesseract: %w (%s)", err, msg)
	}
	return parseTSV(stdout.Bytes(), p.id)
}

func parseTSV(tsv []byte, pass int) ([]cand, error) {
	var out []cand
	lines := bytes.Split(tsv, []byte{'\n'})
	for i, raw := range lines {
		if i == 0 {
			continue
		}
		line := strings.TrimRight(string(raw), "\r")
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 12)
		if len(f) < 12 {
			continue
		}
		level, _ := strconv.Atoi(f[0])
		if level != 5 {
			continue
		}
		text := strings.TrimSpace(f[11])
		if !isWord(text) {
			continue
		}
		conf, err := strconv.ParseFloat(f[10], 64)
		if err != nil || conf < 0 {
			continue
		}
		x, _ := strconv.Atoi(f[6])
		y, _ := strconv.Atoi(f[7])
		bw, _ := strconv.Atoi(f[8])
		bh, _ := strconv.Atoi(f[9])
		if bw < 4 || bh < 4 {
			continue
		}
		out = append(out, cand{text: text, conf: conf, x: x, y: y, w: bw, h: bh, pass: pass})
	}
	return out, nil
}

func isWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// chooseWords collapses the readings of one place into a single word.
// A reading joins a cluster when it overlaps one of that cluster's
// readings, not the cluster's bounding box, so a loose box cannot
// bridge two labels.
func chooseWords(cands []cand) []span {
	var clusters [][]cand
	for _, c := range cands {
		placed := -1
	seek:
		for i, cl := range clusters {
			for _, m := range cl {
				if overlaps(m, c) {
					placed = i
					break seek
				}
			}
		}
		if placed < 0 {
			clusters = append(clusters, []cand{c})
			continue
		}
		clusters[placed] = append(clusters[placed], c)
	}
	var words []span
	for _, cl := range clusters {
		win, ok := choose(cl)
		if !ok {
			continue
		}
		words = append(words, span{text: win.text, x: win.x, y: win.y, w: win.w, h: win.h})
	}
	return words
}

func choose(cl []cand) (cand, bool) {
	h := 0
	for _, c := range cl {
		if c.h > h {
			h = c.h
		}
	}
	if h >= titleHeight {
		for _, c := range cl {
			if c.pass == passTitle {
				return c, true
			}
		}
	}
	best := cand{conf: -1}
	found := false
	for _, c := range cl {
		if c.conf < 40 || c.conf <= best.conf {
			continue
		}
		best = c
		found = true
	}
	if found {
		return best, true
	}
	for _, c := range cl {
		if c.pass == passStroke {
			return c, true
		}
	}
	if len(cl) == 0 {
		return cand{}, false
	}
	return cl[0], true
}

func overlaps(a, b cand) bool {
	if iou(a, b) >= 0.3 {
		return true
	}
	acx, acy := a.x+a.w/2, a.y+a.h/2
	bcx, bcy := b.x+b.w/2, b.y+b.h/2
	return inside(a, bcx, bcy) || inside(b, acx, acy)
}

func inside(c cand, x, y int) bool {
	return x >= c.x && x < c.x+c.w && y >= c.y && y < c.y+c.h
}

func iou(a, b cand) float64 {
	x0 := max(a.x, b.x)
	y0 := max(a.y, b.y)
	x1 := min(a.x+a.w, b.x+b.w)
	y1 := min(a.y+a.h, b.y+b.h)
	if x1 <= x0 || y1 <= y0 {
		return 0
	}
	inter := (x1 - x0) * (y1 - y0)
	union := a.w*a.h + b.w*b.h - inter
	if union <= 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// phrases joins words that sit on one line with a gap no bigger than a
// space. "Example" and "Menu" become "Example Menu"; labels in
// separate buttons do not, because the gap between buttons is wider
// than the letters.
// Why: doc/imgfind.md#how-text-is-read
func phrases(words []span) []span {
	if len(words) == 0 {
		return nil
	}
	order := append([]span(nil), words...)
	sortSpans(order)
	var lines [][]span
	for _, w := range order {
		cy := w.y + w.h/2
		placed := false
		for i := range lines {
			ref := lines[i][0]
			ry := ref.y + ref.h/2
			if abs(cy-ry) <= max(10, max(w.h, ref.h)/2) {
				lines[i] = append(lines[i], w)
				placed = true
				break
			}
		}
		if !placed {
			lines = append(lines, []span{w})
		}
	}
	var out []span
	for _, ln := range lines {
		sortSpans(ln)
		run := []span{ln[0]}
		flush := func() {
			if len(run) >= 2 {
				out = append(out, joinRun(run))
			}
		}
		for _, w := range ln[1:] {
			prev := run[len(run)-1]
			gap := w.x - (prev.x + prev.w)
			if gap < 0 {
				gap = 0
			}
			limit := max(prev.h, w.h) * 3 / 4
			if gap <= limit {
				run = append(run, w)
				continue
			}
			flush()
			run = []span{w}
		}
		flush()
	}
	return out
}

func joinRun(run []span) span {
	parts := make([]string, len(run))
	x0, y0 := run[0].x, run[0].y
	x1, y1 := run[0].x+run[0].w, run[0].y+run[0].h
	for i, w := range run {
		parts[i] = w.text
		x0 = min(x0, w.x)
		y0 = min(y0, w.y)
		x1 = max(x1, w.x+w.w)
		y1 = max(y1, w.y+w.h)
	}
	return span{text: strings.Join(parts, " "), x: x0, y: y0, w: x1 - x0, h: y1 - y0}
}

func sortSpans(s []span) {
	for i := 1; i < len(s); i++ {
		j := i
		for j > 0 {
			a, b := s[j-1], s[j]
			if a.y-b.y > 8 || (abs(a.y-b.y) <= 8 && a.x > b.x) {
				s[j-1], s[j] = s[j], s[j-1]
				j--
				continue
			}
			break
		}
	}
}

func dilate(mask []byte, w, h int) []byte {
	nxt := make([]byte, len(mask))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			on := byte(0)
			for dy := -1; dy <= 1 && on == 0; dy++ {
				for dx := -1; dx <= 1 && on == 0; dx++ {
					xx, yy := x+dx, y+dy
					if xx >= 0 && yy >= 0 && xx < w && yy < h && mask[yy*w+xx] == 1 {
						on = 1
					}
				}
			}
			nxt[y*w+x] = on
		}
	}
	return nxt
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
