package service

import (
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

var productionCodeLanguagePattern = regexp.MustCompile(`^[A-Za-z0-9_+.-]{0,32}$`)

func productionEscapeMarkdown(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`, `*`, `\*`, `_`, `\_`, `{`, `\{`, `}`, `\}`,
		`[`, `\[`, `]`, `\]`, `(`, `\(`, `)`, `\)`, `#`, `\#`,
		`+`, `\+`, `-`, `\-`, `!`, `\!`, `|`, `\|`, "`", "\\`", `~`, `\~`,
	)
	value = html.EscapeString(replacer.Replace(value))
	value = strings.ReplaceAll(value, "\r\n", "<br>")
	value = strings.ReplaceAll(value, "\n", "<br>")
	return strings.ReplaceAll(value, "\r", "<br>")
}

func productionCodeFence(code string) string {
	longest := 0
	current := 0
	for _, char := range code {
		if char == '`' {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	if longest < 3 {
		longest = 2
	}
	return strings.Repeat("`", longest+1)
}

func productionRenderBlock(block *types.ProductionDocumentBlock) (string, []string, error) {
	if block == nil {
		return "", nil, errors.New("cannot render nil block")
	}
	if err := productionValidateBlockContent(block); err != nil {
		return "", nil, fmt.Errorf("cannot render block %s content: %w", block.LogicalBlockID, err)
	}
	attributes, err := productionParseAttributes(block.Attributes)
	if err != nil {
		return "", nil, fmt.Errorf("cannot render block %s attributes: %w", block.LogicalBlockID, err)
	}
	refs, err := productionParseEvidenceRefs(block.EvidenceRefs)
	if err != nil {
		return "", nil, fmt.Errorf("cannot render block %s evidence references: %w", block.LogicalBlockID, err)
	}

	var rendered string
	switch block.BlockType {
	case "heading":
		var text string
		_ = productionDecodeJSON(block.Content, "", &text)
		rendered = strings.Repeat("#", attributes.Level) + " " + productionEscapeMarkdown(text)
	case "paragraph":
		var text string
		_ = productionDecodeJSON(block.Content, "", &text)
		rendered = productionEscapeMarkdown(text)
	case "list":
		var items []string
		_ = productionDecodeJSON(block.Content, "", &items)
		lines := make([]string, 0, len(items))
		for index, item := range items {
			marker := "-"
			if attributes.Ordered {
				marker = fmt.Sprintf("%d.", index+1)
			}
			lines = append(lines, marker+" "+productionEscapeMarkdown(item))
		}
		rendered = strings.Join(lines, "\n")
	case "table":
		var table struct {
			Headers []string   `json:"headers"`
			Rows    [][]string `json:"rows"`
		}
		_ = productionDecodeJSON(block.Content, "", &table)
		lines := []string{"| " + strings.Join(productionEscapedValues(table.Headers), " | ") + " |"}
		separator := make([]string, len(table.Headers))
		for index := range separator {
			separator[index] = "---"
		}
		lines = append(lines, "| "+strings.Join(separator, " | ")+" |")
		for _, row := range table.Rows {
			lines = append(lines, "| "+strings.Join(productionEscapedValues(row), " | ")+" |")
		}
		rendered = strings.Join(lines, "\n")
	case "image":
		var image struct {
			Alt string `json:"alt"`
			URL string `json:"url"`
		}
		_ = productionDecodeJSON(block.Content, "", &image)
		parsed, parseErr := url.Parse(image.URL)
		if parseErr != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
			parsed.User != nil || strings.ContainsAny(image.URL, "?#") {
			return "", nil, fmt.Errorf("cannot render block %s: unsafe image URL", block.LogicalBlockID)
		}
		safeURL := strings.NewReplacer("(", "%28", ")", "%29", "<", "%3C", ">", "%3E").Replace(parsed.String())
		rendered = "![" + productionEscapeMarkdown(image.Alt) + "](" + safeURL + ")"
	case "code":
		if !productionCodeLanguagePattern.MatchString(attributes.Language) {
			return "", nil, fmt.Errorf("cannot render block %s: invalid code language", block.LogicalBlockID)
		}
		var code string
		_ = productionDecodeJSON(block.Content, "", &code)
		fence := productionCodeFence(code)
		rendered = fence + attributes.Language + "\n" + code + "\n" + fence
	case "callout":
		kind := strings.ToUpper(strings.TrimSpace(attributes.Kind))
		if kind == "" {
			kind = "NOTE"
		}
		if !productionCodeLanguagePattern.MatchString(kind) {
			return "", nil, fmt.Errorf("cannot render block %s: invalid callout kind", block.LogicalBlockID)
		}
		var text string
		_ = productionDecodeJSON(block.Content, "", &text)
		rendered = "> **" + kind + "**<br>" + productionEscapeMarkdown(text)
	default:
		return "", nil, fmt.Errorf("cannot render block %s: unsupported block type %q", block.LogicalBlockID, block.BlockType)
	}
	return rendered, refs, nil
}

func productionEscapedValues(values []string) []string {
	escaped := make([]string, len(values))
	for index, value := range values {
		escaped[index] = productionEscapeMarkdown(value)
	}
	return escaped
}

func RenderProductionMarkdown(version *types.ProductionDocumentVersion) (string, error) {
	if version == nil {
		return "", errors.New("production document version is required")
	}
	blocks := productionSortedBlocks(version.Blocks)
	parts := make([]string, 0, len(blocks)+1)
	footnotes := make([]string, 0)
	seenFootnotes := make(map[string]struct{})
	for _, block := range blocks {
		rendered, refs, err := productionRenderBlock(block)
		if err != nil {
			return "", err
		}
		blockRefs := make([]string, 0, len(refs))
		seenBlockRefs := make(map[string]struct{}, len(refs))
		for _, ref := range refs {
			if _, seen := seenBlockRefs[ref]; seen {
				continue
			}
			seenBlockRefs[ref] = struct{}{}
			blockRefs = append(blockRefs, "[^evidence:"+ref+"]")
			if _, seen := seenFootnotes[ref]; !seen {
				seenFootnotes[ref] = struct{}{}
				footnotes = append(footnotes, ref)
			}
		}
		if len(blockRefs) != 0 {
			rendered += " " + strings.Join(blockRefs, " ")
		}
		parts = append(parts, rendered)
	}
	if len(footnotes) != 0 {
		definitions := make([]string, 0, len(footnotes))
		for _, ref := range footnotes {
			definitions = append(definitions, "[^evidence:"+ref+"]: Evidence "+ref)
		}
		parts = append(parts, strings.Join(definitions, "\n"))
	}
	return strings.Join(parts, "\n\n") + "\n", nil
}
