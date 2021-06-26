package editor

import (
	"bytes"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	colorful "github.com/lucasb-eyer/go-colorful"
	runewidth "github.com/mattn/go-runewidth"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"
)

const (
	colorForegroundPos = 1
	colorBackgroundPos = 3
	colorFlagPos       = 5
)

const (
	AlignLeft = iota
	AlignCenter
	AlignRight
)

var (
	openColorRegex  = regexp.MustCompile(`\[([a-zA-Z]*|#[0-9a-zA-Z]*)$`)
	openRegionRegex = regexp.MustCompile(`\["[a-zA-Z0-9_,;: \-\.]*"?$`)
	newLineRegex    = regexp.MustCompile(`\r?\n`)
	regionPattern   = regexp.MustCompile(`\["([a-zA-Z0-9_,;: \-\.]*)"\]`)
	colorPattern    = regexp.MustCompile(`\[([a-zA-Z]+|#[0-9a-zA-Z]{6}|\-)?(:([a-zA-Z]+|#[0-9a-zA-Z]{6}|\-)?(:([lbdru]+|\-)?)?)?\]`)
	escapePattern   = regexp.MustCompile(`\[([a-zA-Z0-9_,;: \-\."#]+)\[(\[*)\]`)
	spacePattern    = regexp.MustCompile(`\s+`)
	boundaryPattern = regexp.MustCompile(`(([,\.\-:;!\?&#+]|\n)[ \t\f\r]*|([ \t\f\r]+))`)

	// TabSize is the number of spaces with which a tab character will be replaced.
	TabSize = 4
)

// overlayStyle calculates a new style based on "style" and applying tag-based
// colors/attributes to it (see also styleFromTag()).
func overlayStyle(style tcell.Style, fgColor, bgColor, attributes string) tcell.Style {
	_, _, defAttr := style.Decompose()

	if fgColor != "" && fgColor != "-" {
		style = style.Foreground(tcell.GetColor(fgColor))
	}

	if bgColor != "" && bgColor != "-" {
		style = style.Background(tcell.GetColor(bgColor))
	}

	if attributes == "-" {
		style = style.Bold(defAttr&tcell.AttrBold > 0)
		style = style.Blink(defAttr&tcell.AttrBlink > 0)
		style = style.Reverse(defAttr&tcell.AttrReverse > 0)
		style = style.Underline(defAttr&tcell.AttrUnderline > 0)
		style = style.Dim(defAttr&tcell.AttrDim > 0)
	} else if attributes != "" {
		style = style.Normal()
		for _, flag := range attributes {
			switch flag {
			case 'l':
				style = style.Blink(true)
			case 'b':
				style = style.Bold(true)
			case 'd':
				style = style.Dim(true)
			case 'r':
				style = style.Reverse(true)
			case 'u':
				style = style.Underline(true)
			}
		}
	}

	return style
}

// iterateString iterates through the given string one printed character at a
// time. For each such character, the callback function is called with the
// Unicode code points of the character (the first rune and any combining runes
// which may be nil if there aren't any), the starting position (in bytes)
// within the original string, its length in bytes, the screen position of the
// character, and the screen width of it. The iteration stops if the callback
// returns true. This function returns true if the iteration was stopped before
// the last character.
func iterateString(text string, callback func(main rune, comb []rune, textPos, textWidth, screenPos, screenWidth int) bool) bool {
	var screenPos int

	gr := uniseg.NewGraphemes(text)
	for gr.Next() {
		r := gr.Runes()
		from, to := gr.Positions()
		width := stringWidth(gr.Str())
		var comb []rune
		if len(r) > 1 {
			comb = r[1:]
		}

		if callback(r[0], comb, from, to-from, screenPos, width) {
			return true
		}

		screenPos += width
	}

	return false
}

// styleFromTag takes the given style, defined by a foreground color (fgColor),
// a background color (bgColor), and style attributes, and modifies it based on
// the substrings (tagSubstrings) extracted by the regular expression for color
// tags. The new colors and attributes are returned where empty strings mean
// "don't modify" and a dash ("-") means "reset to default".
func styleFromTag(fgColor, bgColor, attributes string, tagSubstrings []string) (newFgColor, newBgColor, newAttributes string) {
	if tagSubstrings[colorForegroundPos] != "" {
		color := tagSubstrings[colorForegroundPos]
		if color == "-" {
			fgColor = "-"
		} else if color != "" {
			fgColor = color
		}
	}

	if tagSubstrings[colorBackgroundPos-1] != "" {
		color := tagSubstrings[colorBackgroundPos]
		if color == "-" {
			bgColor = "-"
		} else if color != "" {
			bgColor = color
		}
	}

	if tagSubstrings[colorFlagPos-1] != "" {
		flags := tagSubstrings[colorFlagPos]
		if flags == "-" {
			attributes = "-"
		} else if flags != "" {
			attributes = flags
		}
	}

	return fgColor, bgColor, attributes
}

// stringWidth returns the number of horizontal cells needed to print the given
// text. It splits the text into its grapheme clusters, calculates each
// cluster's width, and adds them up to a total.
func stringWidth(text string) (width int) {
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		var chWidth int
		for _, r := range g.Runes() {
			chWidth = runewidth.RuneWidth(r)
			if chWidth > 0 {
				break // Our best guess at this point is to use the width of the first non-zero-width rune.
			}
		}
		width += chWidth
	}
	return
}

// decomposeString returns information about a string which may contain color
// tags or region tags, depending on which ones are requested to be found. It
// returns the indices of the color tags (as returned by
// re.FindAllStringIndex()), the color tags themselves (as returned by
// re.FindAllStringSubmatch()), the indices of region tags and the region tags
// themselves, the indices of an escaped tags (only if at least color tags or
// region tags are requested), the string stripped by any tags and escaped, and
// the screen width of the stripped string.
func decomposeString(text string, findColors, findRegions bool) (colorIndices [][]int, colors [][]string, regionIndices [][]int, regions [][]string, escapeIndices [][]int, stripped string, width int) {
	// Shortcut for the trivial case.
	if !findColors && !findRegions {
		return nil, nil, nil, nil, nil, text, stringWidth(text)
	}

	// Get positions of any tags.
	if findColors {
		colorIndices = colorPattern.FindAllStringIndex(text, -1)
		colors = colorPattern.FindAllStringSubmatch(text, -1)
	}
	if findRegions {
		regionIndices = regionPattern.FindAllStringIndex(text, -1)
		regions = regionPattern.FindAllStringSubmatch(text, -1)
	}
	escapeIndices = escapePattern.FindAllStringIndex(text, -1)

	// Because the color pattern detects empty tags, we need to filter them out.
	for i := len(colorIndices) - 1; i >= 0; i-- {
		if colorIndices[i][1]-colorIndices[i][0] == 2 {
			colorIndices = append(colorIndices[:i], colorIndices[i+1:]...)
			colors = append(colors[:i], colors[i+1:]...)
		}
	}

	// Make a (sorted) list of all tags.
	allIndices := make([][3]int, 0, len(colorIndices)+len(regionIndices)+len(escapeIndices))
	for indexType, index := range [][][]int{colorIndices, regionIndices, escapeIndices} {
		for _, tag := range index {
			allIndices = append(allIndices, [3]int{tag[0], tag[1], indexType})
		}
	}
	sort.Slice(allIndices, func(i int, j int) bool {
		return allIndices[i][0] < allIndices[j][0]
	})

	// Remove the tags from the original string.
	var from int
	buf := make([]byte, 0, len(text))
	for _, indices := range allIndices {
		if indices[2] == 2 { // Escape sequences are not simply removed.
			buf = append(buf, []byte(text[from:indices[1]-2])...)
			buf = append(buf, ']')
			from = indices[1]
		} else {
			buf = append(buf, []byte(text[from:indices[0]])...)
			from = indices[1]
		}
	}
	buf = append(buf, text[from:]...)
	stripped = string(buf)

	// Get the width of the stripped string.
	width = stringWidth(stripped)

	return
}

// stripTags strips colour tags from the given string. (Region tags are not
// stripped.)
func stripTags(text string) string {
	stripped := colorPattern.ReplaceAllStringFunc(text, func(match string) string {
		if len(match) > 2 {
			return ""
		}
		return match
	})
	return escapePattern.ReplaceAllString(stripped, `[$1$2]`)
}

// textViewIndex contains information about a line displayed in the text view.
type textViewIndex struct {
	Line            int    // The index into the "buffer" slice.
	Pos             int    // The index into the "buffer" string (byte position).
	NextPos         int    // The (byte) index of the next line start within this buffer string.
	Width           int    // The screen width of this line.
	ForegroundColor string // The starting foreground color ("" = don't change, "-" = reset).
	BackgroundColor string // The starting background color ("" = don't change, "-" = reset).
	Attributes      string // The starting attributes ("" = don't change, "-" = reset).
	Region          string // The starting region ID.
}

// textViewRegion contains information about a region.
type textViewRegion struct {
	// The region ID.
	ID string

	// The starting and end screen position of the region as determined the last
	// time Draw() was called. A negative value indicates out-of-rect positions.
	FromX, FromY, ToX, ToY int
}

// EditView is a box which displays text. It implements the io.Writer interface
// so you can stream text to it. This does not trigger a redraw automatically
// but if a handler is installed via SetChangedFunc(), you can cause it to be
// redrawn. (See SetChangedFunc() for more details.)
//
// Navigation
//
// If the text view is scrollable (the default), text is kept in a buffer which
// may be larger than the screen and can be navigated similarly to Vim:
//
//   - h, left arrow: Move left.
//   - l, right arrow: Move right.
//   - j, down arrow: Move down.
//   - k, up arrow: Move up.
//   - g, home: Move to the top.
//   - G, end: Move to the bottom.
//   - Ctrl-F, page down: Move down by one page.
//   - Ctrl-B, page up: Move up by one page.
//
// If the text is not scrollable, any text above the top visible line is
// discarded.
//
// Use SetInputCapture() to override or modify keyboard input.
//
// Colors
//
// If dynamic colors are enabled via SetDynamicColors(), text color can be
// changed dynamically by embedding color strings in square brackets. This works
// the same way as anywhere else. Please see the package documentation for more
// information.
//
// Regions and Highlights
//
// If regions are enabled via SetRegions(), you can define text regions within
// the text and assign region IDs to them. Text regions start with region tags.
// Region tags are square brackets that contain a region ID in double quotes,
// for example:
//
//   We define a ["rg"]region[""] here.
//
// A text region ends with the next region tag. Tags with no region ID ([""])
// don't start new regions. They can therefore be used to mark the end of a
// region. Region IDs must satisfy the following regular expression:
//
//   [a-zA-Z0-9_,;: \-\.]+
//
// Regions can be highlighted by calling the Highlight() function with one or
// more region IDs. This can be used to display search results, for example.
//
// The ScrollToHighlight() function can be used to jump to the currently
// highlighted region once when the text view is drawn the next time.
//
// See https://github.com/rivo/tview/wiki/TextView for an example.
type EditView struct {
	sync.Mutex
	*tview.Box

	currentlySelecting bool
	selectStart        *textViewIndex
	selectEnd          *textViewIndex
	cursor             struct {
		x int
		y int
	}
	screen tcell.Screen

	hasFocus bool

	// The text buffer.
	buffer []string

	// The last bytes that have been received but are not part of the buffer yet.
	recentBytes []byte

	// The processed line index. This is nil if the buffer has changed and needs
	// to be re-indexed.
	index []*textViewIndex

	// The text alignment, one of AlignLeft, AlignCenter, or AlignRight.
	align int

	// Information about visible regions as of the last call to Draw().
	regionInfos []*textViewRegion

	// Indices into the "index" slice which correspond to the first line of the
	// first highlight and the last line of the last highlight. This is calculated
	// during re-indexing. Set to -1 if there is no current highlight.
	fromHighlight, toHighlight int

	// The screen space column of the highlight in its first line. Set to -1 if
	// there is no current highlight.
	posHighlight int

	// A set of region IDs that are currently highlighted.
	highlights map[string]struct{}

	// The last width for which the current text view is drawn.
	lastWidth int

	// The screen width of the longest line in the index (not the buffer).
	longestLine int

	// The index of the first line shown in the text view.
	lineOffset int

	// The maximum number of lines kept in the line index, effectively the
	// latest word-wrapped lines. Ignored if 0.
	maxLines int

	// The height of the content the last time the text view was drawn.
	pageSize int

	// If set to true and if wrap is also true, lines are split at spaces or
	// after punctuation characters.
	wordWrap bool

	// The (starting) color of the text.
	textColor tcell.Color

	// If set to true, the text color can be changed dynamically by piping color
	// strings in square brackets to the text view.
	dynamicColors bool

	// If set to true, region tags can be used to define regions.
	regions bool

	// A temporary flag which, when true, will automatically bring the current
	// highlight(s) into the visible screen.
	scrollToHighlights bool

	// If true, setting new highlights will be a XOR instead of an overwrite
	// operation.
	toggleHighlights bool

	// An optional function which is called when the content of the text view has
	// changed.
	changed func()

	// An optional function which is called when one or more regions were
	// highlighted.
	highlighted func(added, removed, remaining []string)
}

// NewEditView returns a new text view.
func NewEditView() *EditView {
	return &EditView{
		Box:           tview.NewBox(),
		highlights:    make(map[string]struct{}),
		lineOffset:    0,
		align:         tview.AlignLeft,
		textColor:     tview.Styles.PrimaryTextColor,
		regions:       false,
		dynamicColors: false,
	}
}

func (t *EditView) reindexAndChange() {
	if changed := func() func() {
		_, _, width, _ := t.GetInnerRect()
		t.Lock()
		defer t.Unlock()
		t.index = nil
		t.reindexBuffer(width)
		return t.changed
	}(); changed != nil {
		go changed()
	}
}

func (t *EditView) deleteAtIndex(idx *textViewIndex) {
	defer t.reindexAndChange()

	if idx.Pos == idx.NextPos {
		if idx.Line < len(t.buffer)-2 {
			t.buffer = append(
				t.buffer[:idx.Line],
				append(
					[]string{fmt.Sprintf("%s%s", t.buffer[idx.Line], t.buffer[idx.Line+1])},
					t.buffer[idx.Line+2:]...)...)
			return
		} else if idx.Line < len(t.buffer)-1 {
			t.buffer = append(t.buffer[:idx.Line], fmt.Sprintf("%s%s", t.buffer[idx.Line], t.buffer[idx.Line+1]))
			return
		} else {
			return
		}

	}

	line := []rune(t.buffer[idx.Line])
	if len(line) == 0 {
		t.buffer = append(t.buffer[:idx.Line], t.buffer[idx.Line+1:]...)
		return
	}

	t.buffer[idx.Line] = fmt.Sprintf("%s%s", string(line[:idx.Pos]), string(line[idx.Pos+1:]))
}

func (t *EditView) deleteAt(x, y int) {
	if idx := t.indexAt(x, y); idx != nil {
		t.deleteAtIndex(idx)
	}
}

func (t *EditView) writeAtIndex(s string, idx *textViewIndex) {
	if len(s) == 0 {
		return
	}

	// Abstract writing string with newlines as writing separate strings with newlines in between, backwards.
	parts := strings.Split(s, "\r")
	if len(s) > 1 && len(parts) > 1 {
		for i := len(parts) - 1; i >= 0; i-- {
			if len(parts[i]) == 0 {
				continue
			}
			t.writeAtIndex(parts[i], idx)
			if i > 0 {
				t.writeAtIndex("\r", idx)
			}
		}
		return
	}

	defer t.reindexAndChange()

	if idx.Pos == idx.NextPos {
		if s == "\r" {
			after := []string{}
			if idx.Line < len(t.buffer)-1 {
				after = t.buffer[idx.Line+1:]
			}
			t.buffer = append(
				t.buffer[:idx.Line+1],
				append(
					[]string{""},
					after...)...)
			idx.NextPos = idx.Pos
			return
		}
		after := ""
		if idx.Pos < len(t.buffer[idx.Line])-1 {
			after = t.buffer[idx.Line][idx.Pos:]
		}
		t.buffer[idx.Line] = fmt.Sprintf("%s%s%s", t.buffer[idx.Line][:idx.Pos], s, after)
		idx.NextPos += len([]rune(s))
		return
	}

	if s == "\r" {
		afterLines := []string{}
		if idx.Line < len(t.buffer)-1 {
			afterLines = t.buffer[idx.Line+1:]
		}
		afterString := ""
		if idx.Pos < len(t.buffer[idx.Line])-1 {
			afterString = t.buffer[idx.Line][idx.Pos:]
		}
		t.buffer = append(
			t.buffer[:idx.Line],
			append(
				[]string{t.buffer[idx.Line][:idx.Pos]},
				append(
					[]string{afterString},
					afterLines...)...)...)
		idx.NextPos = idx.Pos
		return
	}
	after := ""
	if idx.Pos < len(t.buffer[idx.Line])-1 {
		after = t.buffer[idx.Line][idx.Pos:]
	}
	t.buffer[idx.Line] = fmt.Sprintf("%s%s%s", t.buffer[idx.Line][:idx.Pos], s, after)
	idx.NextPos += len([]rune(s))
}

func (t *EditView) writeAt(s string, x, y int) {
	if idx := t.indexAt(x, y); idx != nil {
		t.writeAtIndex(s, idx)
	}
}

func (t *EditView) cursorCanGoDown() bool {
	_, _, _, height := t.GetInnerRect()
	return t.cursor.y < len(t.index)-t.lineOffset-1 && t.cursor.y < height-1
}

func (t *EditView) cursorCanGoUp() bool {
	return t.cursor.y > 0
}

func (t *EditView) cursorCanGoRight() bool {
	_, _, width, _ := t.GetInnerRect()
	pw := t.practicalWidth(t.cursor.y)
	return t.cursor.x < pw && t.cursor.x < width-1
}

func (t *EditView) cursorCanGoLeft() bool {
	return t.cursor.x > 0
}

func (t *EditView) cursorDown() bool {
	if t.cursorCanGoDown() {
		t.cursor.y++
		return true
	} else if _, _, _, height := t.GetInnerRect(); len(t.index)-t.lineOffset > height/2 {
		t.lineOffset++
		return true
	}
	return false
}

func (t *EditView) cursorUp() bool {
	if t.cursorCanGoUp() {
		t.cursor.y--
		return true
	} else if t.lineOffset > 0 {
		t.lineOffset--
		return true
	}
	return false
}

func (t *EditView) cursorRight() bool {
	if t.cursorCanGoRight() {
		t.cursor.x++
		return true
	} else if t.cursorCanGoDown() {
		t.cursor.y++
		t.cursor.x = 0
		return true
	} else if t.cursor.y+t.lineOffset+1 < len(t.index) {
		t.lineOffset++
		return true
	}
	return false
}

func (t *EditView) cursorLeft() bool {
	if t.cursorCanGoLeft() {
		t.cursor.x--
		return true
	} else if t.cursorCanGoUp() {
		_, _, width, _ := t.GetInnerRect()
		t.cursor.y--
		t.cursor.x = t.practicalWidth(t.cursor.y)
		if t.cursor.x > width-1 {
			t.cursor.x = width - 1
		}
		return true
	} else if t.lineOffset > 0 {
		t.lineOffset--
		return true
	}
	return false
}

func (t *EditView) indentAt(y int) int {
	pw := t.practicalWidth(y)
	for x := 0; x < pw; x++ {
		r, found := t.runeAt(x, y)
		if !found || spacePattern.MatchString(string([]rune{r})) {
			return x
		}
	}
	return pw
}

func (t *EditView) indexAt(x, y int) *textViewIndex {
	y += t.lineOffset
	if y+1 > len(t.index) {
		return nil
	}
	idx := t.index[y]
	if x == idx.Width {
		return &textViewIndex{
			Line:    idx.Line,
			Pos:     idx.NextPos,
			NextPos: idx.NextPos,
		}
	} else if x > idx.Width {
		return nil
	}

	practicalLine := t.buffer[idx.Line][idx.Pos:idx.NextPos]
	colorIndices, _, regionIndices, _, escapeIndices, _, _ := decomposeString(string(practicalLine), true, true)

	peekIndex := 0
	consumedLength := 0
	for {
		for _, colorIndex := range colorIndices {
			if colorIndex[0] == peekIndex {
				peekIndex = colorIndex[1]
			}
		}
		for _, regionIndex := range regionIndices {
			if regionIndex[0] == peekIndex {
				peekIndex = regionIndex[1]
			}
		}
		for _, escapeIndex := range escapeIndices {
			if escapeIndex[0] == peekIndex {
				peekIndex = escapeIndex[1]
			}
		}
		if consumedLength == x {
			break
		}
		peekIndex++
		consumedLength++
	}

	return &textViewIndex{
		Line:    idx.Line,
		Pos:     idx.Pos + peekIndex,
		NextPos: idx.NextPos,
	}
}

func (t *EditView) runeAt(x, y int) (rune, bool) {
	if idx := t.indexAt(x, y); idx != nil {
		if idx.Pos == idx.NextPos {
			return 0, false
		}
		return []rune(t.buffer[idx.Line])[idx.Pos], true
	}
	return 0, false
}

// SetWordWrap sets the flag that, if true and if the "wrap" flag is also true
// (see SetWrap()), wraps the line at spaces or after punctuation marks. Note
// that trailing spaces will not be printed.
//
// This flag is ignored if the "wrap" flag is false.
func (t *EditView) SetWordWrap(wrapOnWords bool) *EditView {
	if t.wordWrap != wrapOnWords {
		t.index = nil
	}
	t.wordWrap = wrapOnWords
	return t
}

// SetMaxLines sets the maximum number of lines for this text view. Lines at the
// beginning of the text will be discarded when the text view is drawn, so as to
// remain below this value. Broken lines via word wrapping are counted
// individually.
//
// Note that GetText() will return the shortened text and may start with color
// and/or region tags that were open at the cutoff point.
//
// A value of 0 (the default) will keep all lines in place.
func (t *EditView) SetMaxLines(maxLines int) *EditView {
	t.maxLines = maxLines
	return t
}

// SetTextAlign sets the text alignment within the text view. This must be
// either AlignLeft, AlignCenter, or AlignRight.
func (t *EditView) SetTextAlign(align int) *EditView {
	if t.align != align {
		t.index = nil
	}
	t.align = align
	return t
}

// SetTextColor sets the initial color of the text (which can be changed
// dynamically by sending color strings in square brackets to the text view if
// dynamic colors are enabled).
func (t *EditView) SetTextColor(color tcell.Color) *EditView {
	t.textColor = color
	return t
}

// SetText sets the text of this text view to the provided string. Previously
// contained text will be removed.
func (t *EditView) SetText(text string) *EditView {
	t.Clear()
	fmt.Fprint(t, text)
	return t
}

// GetText returns the current text of this text view. If "stripAllTags" is set
// to true, any region/color tags are stripped from the text.
func (t *EditView) GetText(stripAllTags bool) string {
	// Get the buffer.
	buffer := make([]string, len(t.buffer), len(t.buffer)+1)
	copy(buffer, t.buffer)
	if !stripAllTags {
		buffer = append(buffer, string(t.recentBytes))
	}

	// Add newlines again.
	text := strings.Join(buffer, "\n")

	// Strip from tags if required.
	if stripAllTags {
		if t.regions {
			text = regionPattern.ReplaceAllString(text, "")
		}
		if t.dynamicColors {
			text = stripTags(text)
		}
		if t.regions && !t.dynamicColors {
			text = escapePattern.ReplaceAllString(text, `[$1$2]`)
		}
	}

	return text
}

// SetDynamicColors sets the flag that allows the text color to be changed
// dynamically. See class description for details.
func (t *EditView) SetDynamicColors(dynamic bool) *EditView {
	if t.dynamicColors != dynamic {
		t.index = nil
	}
	t.dynamicColors = dynamic
	return t
}

// SetRegions sets the flag that allows to define regions in the text. See class
// description for details.
func (t *EditView) SetRegions(regions bool) *EditView {
	if t.regions != regions {
		t.index = nil
	}
	t.regions = regions
	return t
}

// SetChangedFunc sets a handler function which is called when the text of the
// text view has changed. This is useful when text is written to this io.Writer
// in a separate goroutine. Doing so does not automatically cause the screen to
// be refreshed so you may want to use the "changed" handler to redraw the
// screen.
//
// Note that to avoid race conditions or deadlocks, there are a few rules you
// should follow:
//
//   - You can call Application.Draw() from this handler.
//   - You can call EditView.HasFocus() from this handler.
//   - During the execution of this handler, access to any other variables from
//     this primitive or any other primitive must be queued using
//     Application.QueueUpdate().
//
// See package description for details on dealing with concurrency.
func (t *EditView) SetChangedFunc(handler func()) *EditView {
	t.changed = handler
	return t
}

// SetHighlightedFunc sets a handler which is called when the list of currently
// highlighted regions change. It receives a list of region IDs which were newly
// highlighted, those that are not highlighted anymore, and those that remain
// highlighted.
//
// Note that because regions are only determined during drawing, this function
// can only fire for regions that have existed during the last call to Draw().
func (t *EditView) SetHighlightedFunc(handler func(added, removed, remaining []string)) *EditView {
	t.highlighted = handler
	return t
}

// ScrollTo scrolls to the specified row and column (both starting with 0).
func (t *EditView) ScrollTo(row int) *EditView {
	t.lineOffset = row
	return t
}

// ScrollToBeginning scrolls to the top left corner of the text if the text view
// is scrollable.
func (t *EditView) ScrollToBeginning() *EditView {
	t.lineOffset = 0
	return t
}

// ScrollToEnd scrolls to the bottom left corner of the text if the text view
// is scrollable. Adding new rows to the end of the text view will cause it to
// scroll with the new data.
func (t *EditView) ScrollToEnd() *EditView {
	_, _, _, height := t.GetInnerRect()
	t.lineOffset = len(t.index) - height
	return t
}

// GetScrollOffset returns the number of rows and columns that are skipped at
// the top left corner when the text view has been scrolled.
func (t *EditView) GetScrollOffset() int {
	return t.lineOffset
}

// Clear removes all text from the buffer.
func (t *EditView) Clear() *EditView {
	t.buffer = nil
	t.recentBytes = nil
	t.index = nil
	return t
}

// Highlight specifies which regions should be highlighted. If highlight
// toggling is set to true (see SetToggleHighlights()), the highlight of the
// provided regions is toggled (highlighted regions are un-highlighted and vice
// versa). If toggling is set to false, the provided regions are highlighted and
// all other regions will not be highlighted (you may also provide nil to turn
// off all highlights).
//
// For more information on regions, see class description. Empty region strings
// are ignored.
//
// Text in highlighted regions will be drawn inverted, i.e. with their
// background and foreground colors swapped.
func (t *EditView) Highlight(regionIDs ...string) *EditView {
	// Toggle highlights.
	if t.toggleHighlights {
		var newIDs []string
	HighlightLoop:
		for regionID := range t.highlights {
			for _, id := range regionIDs {
				if regionID == id {
					continue HighlightLoop
				}
			}
			newIDs = append(newIDs, regionID)
		}
		for _, regionID := range regionIDs {
			if _, ok := t.highlights[regionID]; !ok {
				newIDs = append(newIDs, regionID)
			}
		}
		regionIDs = newIDs
	} // Now we have a list of region IDs that end up being highlighted.

	// Determine added and removed regions.
	var added, removed, remaining []string
	if t.highlighted != nil {
		for _, regionID := range regionIDs {
			if _, ok := t.highlights[regionID]; ok {
				remaining = append(remaining, regionID)
				delete(t.highlights, regionID)
			} else {
				added = append(added, regionID)
			}
		}
		for regionID := range t.highlights {
			removed = append(removed, regionID)
		}
	}

	// Make new selection.
	t.highlights = make(map[string]struct{})
	for _, id := range regionIDs {
		if id == "" {
			continue
		}
		t.highlights[id] = struct{}{}
	}
	t.index = nil

	// Notify.
	if t.highlighted != nil && len(added) > 0 || len(removed) > 0 {
		t.highlighted(added, removed, remaining)
	}

	return t
}

// GetHighlights returns the IDs of all currently highlighted regions.
func (t *EditView) GetHighlights() (regionIDs []string) {
	for id := range t.highlights {
		regionIDs = append(regionIDs, id)
	}
	return
}

// SetToggleHighlights sets a flag to determine how regions are highlighted.
// When set to true, the Highlight() function (or a mouse click) will toggle the
// provided/selected regions. When set to false, Highlight() (or a mouse click)
// will simply highlight the provided regions.
func (t *EditView) SetToggleHighlights(toggle bool) *EditView {
	t.toggleHighlights = toggle
	return t
}

// ScrollToHighlight will cause the visible area to be scrolled so that the
// highlighted regions appear in the visible area of the text view. This
// repositioning happens the next time the text view is drawn. It happens only
// once so you will need to call this function repeatedly to always keep
// highlighted regions in view.
//
// Nothing happens if there are no highlighted regions or if the text view is
// not scrollable.
func (t *EditView) ScrollToHighlight() *EditView {
	if len(t.highlights) == 0 || !t.regions {
		return t
	}
	t.index = nil
	t.scrollToHighlights = true
	return t
}

// GetRegionText returns the text of the region with the given ID. If dynamic
// colors are enabled, color tags are stripped from the text. Newlines are
// always returned as '\n' runes.
//
// If the region does not exist or if regions are turned off, an empty string
// is returned.
func (t *EditView) GetRegionText(regionID string) string {
	if !t.regions || regionID == "" {
		return ""
	}

	var (
		buffer          bytes.Buffer
		currentRegionID string
	)

	for _, str := range t.buffer {
		// Find all color tags in this line.
		var colorTagIndices [][]int
		if t.dynamicColors {
			colorTagIndices = colorPattern.FindAllStringIndex(str, -1)
		}

		// Find all regions in this line.
		var (
			regionIndices [][]int
			regions       [][]string
		)
		if t.regions {
			regionIndices = regionPattern.FindAllStringIndex(str, -1)
			regions = regionPattern.FindAllStringSubmatch(str, -1)
		}

		// Analyze this line.
		var currentTag, currentRegion int
		for pos, ch := range str {
			// Skip any color tags.
			if currentTag < len(colorTagIndices) && pos >= colorTagIndices[currentTag][0] && pos < colorTagIndices[currentTag][1] {
				if pos == colorTagIndices[currentTag][1]-1 {
					currentTag++
				}
				if colorTagIndices[currentTag][1]-colorTagIndices[currentTag][0] > 2 {
					continue
				}
			}

			// Skip any regions.
			if currentRegion < len(regionIndices) && pos >= regionIndices[currentRegion][0] && pos < regionIndices[currentRegion][1] {
				if pos == regionIndices[currentRegion][1]-1 {
					if currentRegionID == regionID {
						// This is the end of the requested region. We're done.
						return buffer.String()
					}
					currentRegionID = regions[currentRegion][1]
					currentRegion++
				}
				continue
			}

			// Add this rune.
			if currentRegionID == regionID {
				buffer.WriteRune(ch)
			}
		}

		// Add newline.
		if currentRegionID == regionID {
			buffer.WriteRune('\n')
		}
	}

	return escapePattern.ReplaceAllString(buffer.String(), `[$1$2]`)
}

// Focus is called when this primitive receives focus.
func (t *EditView) Focus(delegate func(p tview.Primitive)) {
	// Implemented here with locking because this is used by layout primitives.
	t.Lock()
	defer t.Unlock()
	t.Box.Focus(delegate)
}

// HasFocus returns whether or not this primitive has focus.
func (t *EditView) HasFocus() bool {
	// Implemented here with locking because this may be used in the "changed"
	// callback.
	t.Lock()
	defer t.Unlock()
	return t.Box.HasFocus()
}

func (t *EditView) Blur() {
	t.Lock()
	defer t.Unlock()
	t.Box.Blur()
}

// Write lets us implement the io.Writer interface. Tab characters will be
// replaced with TabSize space characters. A "\n" or "\r\n" will be interpreted
// as a new line.
func (t *EditView) Write(p []byte) (n int, err error) {
	// Notify at the end.
	t.Lock()
	changed := t.changed
	t.Unlock()
	if changed != nil {
		defer func() {
			// We always call the "changed" function in a separate goroutine to avoid
			// deadlocks.
			go changed()
		}()
	}

	t.Lock()
	defer t.Unlock()

	// Copy data over.
	newBytes := append(t.recentBytes, p...)
	t.recentBytes = nil

	// If we have a trailing invalid UTF-8 byte, we'll wait.
	if r, _ := utf8.DecodeLastRune(p); r == utf8.RuneError {
		t.recentBytes = newBytes
		return len(p), nil
	}

	// If we have a trailing open dynamic color, exclude it.
	if t.dynamicColors {
		location := openColorRegex.FindIndex(newBytes)
		if location != nil {
			t.recentBytes = newBytes[location[0]:]
			newBytes = newBytes[:location[0]]
		}
	}

	// If we have a trailing open region, exclude it.
	if t.regions {
		location := openRegionRegex.FindIndex(newBytes)
		if location != nil {
			t.recentBytes = newBytes[location[0]:]
			newBytes = newBytes[:location[0]]
		}
	}

	// Transform the new bytes into strings.
	newBytes = bytes.Replace(newBytes, []byte{'\t'}, bytes.Repeat([]byte{' '}, TabSize), -1)
	for index, line := range newLineRegex.Split(string(newBytes), -1) {
		if index == 0 {
			if len(t.buffer) == 0 {
				t.buffer = []string{line}
			} else {
				t.buffer[len(t.buffer)-1] += line
			}
		} else {
			t.buffer = append(t.buffer, line)
		}
	}

	// Reset the index.
	t.index = nil

	return len(p), nil
}

// reindexBuffer re-indexes the buffer such that we can use it to easily draw
// the buffer onto the screen. Each line in the index will contain a pointer
// into the buffer from which on we will print text. It will also contain the
// colors, attributes, and region with which the line starts.
//
// If maxLines is greater than 0, any extra lines will be dropped from the
// buffer.
func (t *EditView) reindexBuffer(width int) {
	if t.index != nil {
		return // Nothing has changed. We can still use the current index.
	}
	t.index = nil
	t.fromHighlight, t.toHighlight, t.posHighlight = -1, -1, -1

	// If there's no space, there's no index.
	if width < 1 {
		return
	}

	// Initial states.
	regionID := ""
	var (
		highlighted                                  bool
		foregroundColor, backgroundColor, attributes string
	)

	// Go through each line in the buffer.
	for bufferIndex, str := range t.buffer {
		colorTagIndices, colorTags, regionIndices, regions, escapeIndices, strippedStr, _ := decomposeString(str, t.dynamicColors, t.regions)

		// Split the line if required.
		var splitLines []string
		str = strippedStr
		if len(str) > 0 {
			for len(str) > 0 {
				extract := runewidth.Truncate(str, width, "")
				if len(extract) == 0 {
					// We'll extract at least one grapheme cluster.
					gr := uniseg.NewGraphemes(str)
					gr.Next()
					_, to := gr.Positions()
					extract = str[:to]
				}
				if t.wordWrap && len(extract) < len(str) {
					// Add any spaces from the next line.
					if spaces := spacePattern.FindStringIndex(str[len(extract):]); spaces != nil && spaces[0] == 0 {
						extract = str[:len(extract)+spaces[1]]
					}

					// Can we split before the mandatory end?
					matches := boundaryPattern.FindAllStringIndex(extract, -1)
					if len(matches) > 0 {
						// Yes. Let's split there.
						extract = extract[:matches[len(matches)-1][1]]
					}
				}
				splitLines = append(splitLines, extract)
				str = str[len(extract):]
			}
		} else {
			// No need to split the line.
			splitLines = []string{str}
		}

		// Create index from split lines.
		var originalPos, colorPos, regionPos, escapePos int
		for _, splitLine := range splitLines {
			line := &textViewIndex{
				Line:            bufferIndex,
				Pos:             originalPos,
				ForegroundColor: foregroundColor,
				BackgroundColor: backgroundColor,
				Attributes:      attributes,
				Region:          regionID,
			}

			// Shift original position with tags.
			lineLength := len(splitLine)
			remainingLength := lineLength
			tagEnd := originalPos
			totalTagLength := 0
			for {
				// Which tag comes next?
				nextTag := make([][3]int, 0, 3)
				if colorPos < len(colorTagIndices) {
					nextTag = append(nextTag, [3]int{colorTagIndices[colorPos][0], colorTagIndices[colorPos][1], 0}) // 0 = color tag.
				}
				if regionPos < len(regionIndices) {
					nextTag = append(nextTag, [3]int{regionIndices[regionPos][0], regionIndices[regionPos][1], 1}) // 1 = region tag.
				}
				if escapePos < len(escapeIndices) {
					nextTag = append(nextTag, [3]int{escapeIndices[escapePos][0], escapeIndices[escapePos][1], 2}) // 2 = escape tag.
				}
				minPos := -1
				tagIndex := -1
				for index, pair := range nextTag {
					if minPos < 0 || pair[0] < minPos {
						minPos = pair[0]
						tagIndex = index
					}
				}

				// Is the next tag in range?
				if tagIndex < 0 || minPos > tagEnd+remainingLength {
					break // No. We're done with this line.
				}

				// Advance.
				strippedTagStart := nextTag[tagIndex][0] - originalPos - totalTagLength
				tagEnd = nextTag[tagIndex][1]
				tagLength := tagEnd - nextTag[tagIndex][0]
				if nextTag[tagIndex][2] == 2 {
					tagLength = 1
				}
				totalTagLength += tagLength
				remainingLength = lineLength - (tagEnd - originalPos - totalTagLength)

				// Process the tag.
				switch nextTag[tagIndex][2] {
				case 0:
					// Process color tags.
					foregroundColor, backgroundColor, attributes = styleFromTag(foregroundColor, backgroundColor, attributes, colorTags[colorPos])
					colorPos++
				case 1:
					// Process region tags.
					regionID = regions[regionPos][1]
					_, highlighted = t.highlights[regionID]

					// Update highlight range.
					if highlighted {
						line := len(t.index)
						if t.fromHighlight < 0 {
							t.fromHighlight, t.toHighlight = line, line
							t.posHighlight = stringWidth(splitLine[:strippedTagStart])
						} else if line > t.toHighlight {
							t.toHighlight = line
						}
					}

					regionPos++
				case 2:
					// Process escape tags.
					escapePos++
				}
			}

			// Advance to next line.
			originalPos += lineLength + totalTagLength

			// Append this line.
			line.NextPos = originalPos
			line.Width = stringWidth(splitLine)
			t.index = append(t.index, line)
		}

		// Word-wrapped lines may have trailing whitespace. Remove it.
		if t.wordWrap {
			for _, line := range t.index {
				str := t.buffer[line.Line][line.Pos:line.NextPos]
				spaces := spacePattern.FindAllStringIndex(str, -1)
				if spaces != nil && spaces[len(spaces)-1][1] == len(str) {
					oldNextPos := line.NextPos
					line.NextPos -= spaces[len(spaces)-1][1] - spaces[len(spaces)-1][0]
					line.Width -= stringWidth(t.buffer[line.Line][line.NextPos:oldNextPos])
				}
			}
		}
	}

	// Drop lines beyond maxLines.
	if t.maxLines > 0 && len(t.index) > t.maxLines {
		removedLines := len(t.index) - t.maxLines

		// Adjust the index.
		t.index = t.index[removedLines:]
		if t.fromHighlight >= 0 {
			t.fromHighlight -= removedLines
			if t.fromHighlight < 0 {
				t.fromHighlight = 0
			}
		}
		if t.toHighlight >= 0 {
			t.toHighlight -= removedLines
			if t.toHighlight < 0 {
				t.fromHighlight, t.toHighlight, t.posHighlight = -1, -1, -1
			}
		}
		bufferShift := t.index[0].Line
		for _, line := range t.index {
			line.Line -= bufferShift
		}

		// Adjust the original buffer.
		t.buffer = t.buffer[bufferShift:]
		var prefix string
		if t.index[0].ForegroundColor != "" || t.index[0].BackgroundColor != "" || t.index[0].Attributes != "" {
			prefix = fmt.Sprintf("[%s:%s:%s]", t.index[0].ForegroundColor, t.index[0].BackgroundColor, t.index[0].Attributes)
		}
		if t.index[0].Region != "" {
			prefix += fmt.Sprintf(`["%s"]`, t.index[0].Region)
		}
		posShift := t.index[0].Pos
		t.buffer[0] = prefix + t.buffer[0][posShift:]
		t.lineOffset -= removedLines
		if t.lineOffset < 0 {
			t.lineOffset = 0
		}

		// Adjust positions of first buffer line.
		posShift -= len(prefix)
		for _, line := range t.index {
			if line.Line != 0 {
				break
			}
			line.Pos -= posShift
			line.NextPos -= posShift
		}
	}

	// Calculate longest line.
	t.longestLine = 0
	for _, line := range t.index {
		if line.Width > t.longestLine {
			t.longestLine = line.Width
		}
	}
}

// Draw draws this primitive onto the screen.
func (t *EditView) Draw(screen tcell.Screen) {
	t.Box.DrawForSubclass(screen, t)
	t.Lock()
	defer t.Unlock()
	t.screen = screen
	totalWidth, totalHeight := screen.Size()

	// Get the available size.
	x, y, width, height := t.GetInnerRect()
	t.pageSize = height

	// If the width has changed, we need to reindex.
	if width != t.lastWidth {
		t.index = nil
	}
	t.lastWidth = width

	// Re-index.
	t.reindexBuffer(width)
	if t.regions {
		t.regionInfos = nil
	}

	// If we don't have an index, there's nothing to draw.
	if t.index == nil {
		return
	}

	// Move to highlighted regions.
	if t.regions && t.scrollToHighlights && t.fromHighlight >= 0 {
		// Do we fit the entire height?
		if t.toHighlight-t.fromHighlight+1 < height {
			// Yes, let's center the highlights.
			t.lineOffset = (t.fromHighlight + t.toHighlight - height) / 2
		} else {
			// No, let's move to the start of the highlights.
			t.lineOffset = t.fromHighlight
		}
	}
	t.scrollToHighlights = false

	if t.lineOffset < 0 {
		t.lineOffset = 0
	}

	// Draw the buffer.
	defaultStyle := tcell.StyleDefault.Foreground(t.textColor).Background(t.Box.GetBackgroundColor())
	for line := t.lineOffset; line < len(t.index); line++ {
		// Are we done?
		if line-t.lineOffset >= height || y+line-t.lineOffset >= totalHeight {
			break
		}

		// Get the text for this line.
		index := t.index[line]
		text := t.buffer[index.Line][index.Pos:index.NextPos]
		foregroundColor := index.ForegroundColor
		backgroundColor := index.BackgroundColor
		attributes := index.Attributes
		regionID := index.Region
		if t.regions {
			if len(t.regionInfos) > 0 && t.regionInfos[len(t.regionInfos)-1].ID != regionID {
				// End last region.
				t.regionInfos[len(t.regionInfos)-1].ToX = x
				t.regionInfos[len(t.regionInfos)-1].ToY = y + line - t.lineOffset
			}
			if regionID != "" && (len(t.regionInfos) == 0 || t.regionInfos[len(t.regionInfos)-1].ID != regionID) {
				// Start a new region.
				t.regionInfos = append(t.regionInfos, &textViewRegion{
					ID:    regionID,
					FromX: x,
					FromY: y + line - t.lineOffset,
					ToX:   -1,
					ToY:   -1,
				})
			}
		}

		// Process tags.
		colorTagIndices, colorTags, regionIndices, regions, escapeIndices, strippedText, _ := decomposeString(text, t.dynamicColors, t.regions)

		// Calculate the position of the line.
		var posX int

		// Print the line.
		if y+line-t.lineOffset >= 0 {
			var colorPos, regionPos, escapePos, tagOffset int
			iterateString(strippedText, func(main rune, comb []rune, textPos, textWidth, screenPos, screenWidth int) bool {
				// Process tags.
				for {
					if colorPos < len(colorTags) && textPos+tagOffset >= colorTagIndices[colorPos][0] && textPos+tagOffset < colorTagIndices[colorPos][1] {
						// Get the color.
						foregroundColor, backgroundColor, attributes = styleFromTag(foregroundColor, backgroundColor, attributes, colorTags[colorPos])
						tagOffset += colorTagIndices[colorPos][1] - colorTagIndices[colorPos][0]
						colorPos++
					} else if regionPos < len(regionIndices) && textPos+tagOffset >= regionIndices[regionPos][0] && textPos+tagOffset < regionIndices[regionPos][1] {
						// Get the region.
						if regionID != "" && len(t.regionInfos) > 0 && t.regionInfos[len(t.regionInfos)-1].ID == regionID {
							// End last region.
							t.regionInfos[len(t.regionInfos)-1].ToX = x + posX
							t.regionInfos[len(t.regionInfos)-1].ToY = y + line - t.lineOffset
						}
						regionID = regions[regionPos][1]
						if regionID != "" {
							// Start new region.
							t.regionInfos = append(t.regionInfos, &textViewRegion{
								ID:    regionID,
								FromX: x + posX,
								FromY: y + line - t.lineOffset,
								ToX:   -1,
								ToY:   -1,
							})
						}
						tagOffset += regionIndices[regionPos][1] - regionIndices[regionPos][0]
						regionPos++
					} else {
						break
					}
				}

				// Skip the second-to-last character of an escape tag.
				if escapePos < len(escapeIndices) && textPos+tagOffset == escapeIndices[escapePos][1]-2 {
					tagOffset++
					escapePos++
				}

				// Mix the existing style with the new style.
				style := overlayStyle(defaultStyle, foregroundColor, backgroundColor, attributes)

				// Do we highlight this character?
				var highlighted bool
				if regionID != "" {
					if _, ok := t.highlights[regionID]; ok {
						highlighted = true
					}
				}
				if highlighted {
					fg, bg, _ := style.Decompose()
					if bg == t.Box.GetBackgroundColor() {
						r, g, b := fg.RGB()
						c := colorful.Color{R: float64(r) / 255, G: float64(g) / 255, B: float64(b) / 255}
						_, _, li := c.Hcl()
						if li < .5 {
							bg = tcell.ColorWhite
						} else {
							bg = tcell.ColorBlack
						}
					}
					style = style.Background(fg).Foreground(bg)
				}

				// Stop at the right border.
				if posX+screenWidth > width || x+posX >= totalWidth {
					return true
				}

				// Draw the character.
				for offset := screenWidth - 1; offset >= 0; offset-- {
					if offset == 0 {
						screen.SetContent(x+posX+offset, y+line-t.lineOffset, main, comb, style)
					} else {
						screen.SetContent(x+posX+offset, y+line-t.lineOffset, ' ', nil, style)
					}
				}

				// Advance.
				posX += screenWidth
				return false
			})
		}
	}

	// update cursor
	if len(t.index) > 0 {
		limit(&t.cursor.y, 0, min(len(t.index)-t.lineOffset, height))
		limit(&t.cursor.x, 0, min(t.practicalWidth(t.cursor.y)+1, width))
		screen.ShowCursor(x+t.cursor.x, y+t.cursor.y)
	}

}

func min(i ...int) int {
	m := int(math.MaxInt64)
	for _, v := range i {
		if v < m {
			m = v
		}
	}
	return m
}

func max(i ...int) int {
	m := int(math.MinInt64)
	for _, v := range i {
		if v > m {
			m = v
		}
	}
	return m
}

func limit(v *int, minInc, maxExc int) {
	if *v < minInc {
		*v = minInc
	}
	if *v > maxExc-1 {
		*v = maxExc - 1
	}
}

func (t *EditView) practicalWidth(y int) int {
	_, _, width, _ := t.GetInnerRect()
	for width > 0 {
		if idx := t.indexAt(width, y); idx != nil {
			return width
		}
		width--
	}
	return width
}

// InputHandler returns the handler for this primitive.
func (t *EditView) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return t.WrapInputHandler(func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
		if event.Key() == 127 && event.Rune() == 127 {
			if t.cursorLeft() {
				t.deleteAt(t.cursor.x, t.cursor.y)
			}
			return
		}

		_, _, _, height := t.GetInnerRect()

		stopSelecting := true
		key := event.Key()
		switch key {
		case tcell.KeyDelete:
			t.deleteAt(t.cursor.x, t.cursor.y)
		case tcell.KeyHome:
			if event.Modifiers()&tcell.ModShift != 0 {
				stopSelecting = false
			}
			t.lineOffset = 0
			t.cursor.x = 0
			t.cursor.y = 0
		case tcell.KeyPgUp:
			if event.Modifiers()&tcell.ModShift != 0 {
				stopSelecting = false
			}
			for i := 0; i < height; i++ {
				if !t.cursorUp() {
					break
				}
			}
		case tcell.KeyPgDn:
			for i := 0; i < height; i++ {
				if !t.cursorDown() {
					break
				}
			}
		case tcell.KeyEnd:
			if event.Modifiers()&tcell.ModShift != 0 {
				stopSelecting = false
			}
			t.lineOffset = len(t.index) - height/2
			t.cursor.y = len(t.index) - t.lineOffset - 1
			t.cursor.x = t.practicalWidth(t.cursor.y)
		case tcell.KeyUp:
			if event.Modifiers()&tcell.ModShift != 0 {
				stopSelecting = false
			}
			if event.Modifiers()&tcell.ModCtrl != 0 {
				indent := t.indentAt(t.cursor.y)
				for t.cursorUp() {
					if t.indentAt(t.cursor.y) != indent {
						break
					}
				}
			} else {
				t.cursorUp()
			}
		case tcell.KeyDown:
			if event.Modifiers()&tcell.ModShift != 0 {
				stopSelecting = false
			}
			if event.Modifiers()&tcell.ModCtrl != 0 {
				indent := t.indentAt(t.cursor.y)
				for t.cursorDown() {
					if t.indentAt(t.cursor.y) != indent {
						break
					}
				}
			} else {
				t.cursorDown()
			}
		case tcell.KeyLeft:
			if event.Modifiers()&tcell.ModShift != 0 {
				stopSelecting = false
			}
			if event.Modifiers()&tcell.ModCtrl != 0 {
				r, origFound := t.runeAt(t.cursor.x, t.cursor.y)
				isWhite := spacePattern.MatchString(string([]rune{r}))
				for t.cursorLeft() {
					r, found := t.runeAt(t.cursor.x, t.cursor.y)
					if isWhite != spacePattern.MatchString(string([]rune{r})) || found != origFound {
						break
					}
				}
			} else {
				t.cursorLeft()
			}
		case tcell.KeyRight:
			if event.Modifiers()&tcell.ModShift != 0 {
				stopSelecting = false
			}
			if event.Modifiers()&tcell.ModCtrl != 0 {
				r, origFound := t.runeAt(t.cursor.x, t.cursor.y)
				isWhite := spacePattern.MatchString(string([]rune{r}))
				for t.cursorRight() {
					r, found := t.runeAt(t.cursor.x, t.cursor.y)
					if isWhite != spacePattern.MatchString(string([]rune{r})) || found != origFound {
						break
					}
				}
			} else {
				t.cursorRight()
			}
		default:
			t.writeAt(string([]rune{event.Rune()}), t.cursor.x, t.cursor.y)
			t.cursorRight()
		}
		if t.currentlySelecting {
			if stopSelecting {
				t.currentlySelecting = false
			} else {
				t.removeHighlight(t.selectStart, t.selectEnd)
				t.selectEnd = t.indexAt(t.cursor.x, t.cursor.y)
				t.addHighlight(t.selectStart, t.selectEnd)
			}
		} else {
			if !stopSelecting {
				if t.selectStart = t.indexAt(t.cursor.x, t.cursor.y); t.selectStart != nil {
					t.currentlySelecting = true
					t.selectEnd = t.selectStart
				}
			}
		}
	})
}

const (
	startHighlight = "[white:black]"
	endHighlight   = "[black:white]"
)

func (t *EditView) removeHighlight(start, end *textViewIndex) {
	for _ = range []rune(startHighlight) {
		//t.deleteAt
	}
}

func (t *EditView) addHighlight(start, end *textViewIndex) {
	t.writeAtIndex(startHighlight, start)
	t.writeAtIndex(endHighlight, end)
}

// MouseHandler returns the mouse handler for this primitive.
func (t *EditView) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
	return t.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
		x, y := event.Position()
		if !t.InRect(x, y) {
			return false, nil
		}

		switch action {
		case tview.MouseLeftClick:
			if t.regions {
				// Find a region to highlight.
				for _, region := range t.regionInfos {
					if y == region.FromY && x < region.FromX ||
						y == region.ToY && x >= region.ToX ||
						region.FromY >= 0 && y < region.FromY ||
						region.ToY >= 0 && y > region.ToY {
						continue
					}
					t.Highlight(region.ID)
					break
				}
			}
			setFocus(t)
			consumed = true
		case tview.MouseScrollUp:
			t.lineOffset--
			consumed = true
		case tview.MouseScrollDown:
			t.lineOffset++
			consumed = true
		}

		return
	})
}
