package service

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestProductionMarkdownRendererIsDeterministicPositionOrderedAndDeduplicatesFootnotes(t *testing.T) {
	version := &types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{
		productionValidationBlock("p", "paragraph", 2, `"Second"`, `{}`, `["e-2","e-1"]`),
		productionValidationBlock("h", "heading", 1, `"First"`, `{"level":2}`, `["e-1"]`),
	}}

	first, err := RenderProductionMarkdown(version)
	require.NoError(t, err)
	second, err := RenderProductionMarkdown(version)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Less(t, strings.Index(first, "## First"), strings.Index(first, "Second"))
	require.Contains(t, first, "[^evidence:e-1]")
	require.Equal(t, 1, strings.Count(first, "[^evidence:e-1]:"))
	require.Less(t, strings.Index(first, "[^evidence:e-1]:"), strings.Index(first, "[^evidence:e-2]:"))
	require.NotContains(t, first, "resource://")
	require.NotContains(t, first, "StoragePath")
}

func TestProductionMarkdownRendererStructuresAllSupportedBlockTypes(t *testing.T) {
	version := &types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{
		productionValidationBlock("h", "heading", 0, `"Heading"`, `{"level":3}`, `[]`),
		productionValidationBlock("p", "paragraph", 1, `"Paragraph"`, `{}`, `[]`),
		productionValidationBlock("l", "list", 2, `["one","two"]`, `{"ordered":true}`, `[]`),
		productionValidationBlock("t", "table", 3, `{"headers":["A","B"],"rows":[["1","2"]]}`, `{}`, `[]`),
		productionValidationBlock("i", "image", 4, `{"alt":"diagram","url":"https://example.com/a.png"}`, `{}`, `[]`),
		productionValidationBlock("c", "code", 5, `"fmt.Println(\"ok\")"`, `{"language":"go"}`, `[]`),
		productionValidationBlock("n", "callout", 6, `"Check this"`, `{"kind":"warning"}`, `[]`),
	}}

	markdown, err := RenderProductionMarkdown(version)
	require.NoError(t, err)
	require.Contains(t, markdown, "### Heading")
	require.Contains(t, markdown, "1. one\n2. two")
	require.Contains(t, markdown, "| A | B |")
	require.Contains(t, markdown, "![diagram](https://example.com/a.png)")
	require.Contains(t, markdown, "```go\nfmt.Println")
	require.Contains(t, markdown, "> **WARNING**")
}

func TestProductionMarkdownRendererEscapesMarkdownAndHTMLInjectionBoundaries(t *testing.T) {
	version := &types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{
		productionValidationBlock("h", "heading", 0, `"Title\n# injected"`, `{"level":2}`, `[]`),
		productionValidationBlock("p", "paragraph", 1, `"<script>alert(1)</script> [link](javascript:alert(1))"`, `{}`, `[]`),
		productionValidationBlock("t", "table", 2, `{"headers":["A|B"],"rows":[["line\nnext"]]}`, `{}`, `[]`),
	}}

	markdown, err := RenderProductionMarkdown(version)
	require.NoError(t, err)
	require.NotContains(t, markdown, "<script>")
	require.NotContains(t, markdown, "\n# injected")
	require.Contains(t, markdown, "&lt;script&gt;")
	require.Contains(t, markdown, `A\|B`)
	require.Contains(t, markdown, "line<br>next")
}

func TestProductionMarkdownRendererRejectsUnsafeImageAndEvidenceInjection(t *testing.T) {
	unsafeImage := &types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{
		productionValidationBlock("i", "image", 0, `{"alt":"x","url":"javascript:alert(1)"}`, `{}`, `[]`),
	}}
	_, err := RenderProductionMarkdown(unsafeImage)
	require.ErrorContains(t, err, "image URL")

	unsafeEvidence := &types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{
		productionValidationBlock("p", "paragraph", 0, `"x"`, `{}`, `["e-1]\n[^owned]: injected"]`),
	}}
	_, err = RenderProductionMarkdown(unsafeEvidence)
	require.Error(t, err)
}

func TestProductionMarkdownRendererRejectsImageURLSecretsAndNonPublicReferences(t *testing.T) {
	urls := []string{
		"https://user:secret@example.com/a.png",
		"https://example.com/a.png?token=secret",
		"https://example.com/a.png?",
		"https://example.com/a.png#signed-fragment",
		"https://example.com/a.png#",
		"resource://abcdefghijklmnopqrstuv",
	}
	for _, imageURL := range urls {
		t.Run(imageURL, func(t *testing.T) {
			version := &types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{
				productionValidationBlock("i", "image", 0, `{"alt":"x","url":"`+imageURL+`"}`, `{}`, `[]`),
			}}
			_, err := RenderProductionMarkdown(version)
			require.ErrorContains(t, err, "image URL")
		})
	}
}

func TestProductionMarkdownRendererEscapesEntitiesAndApostrophesFaithfully(t *testing.T) {
	version := &types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{
		productionValidationBlock("p", "paragraph", 0, `"Tom & Jerry's <tag> &lt;literal&gt;"`, `{}`, `[]`),
	}}

	markdown, err := RenderProductionMarkdown(version)
	require.NoError(t, err)
	require.Contains(t, markdown, "Tom &amp; Jerry&#39;s &lt;tag&gt; &amp;lt;literal&amp;gt;")
	require.NotContains(t, markdown, `&\#39;`)
}

func TestProductionMarkdownRendererEscapesUserFenceAndQuoteDelimitersWithoutChangingGeneratedStructure(t *testing.T) {
	paragraph := strconv.Quote("paragraph\n```\n~~~\n> quote")
	heading := strconv.Quote("heading\n```\n~~~\n> quote")
	item := strconv.Quote("item\n```\n~~~\n> quote")
	cell := strconv.Quote("cell\n```\n~~~\n> quote")
	note := strconv.Quote("note\n```\n~~~\n> quote")
	version := &types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{
		productionValidationBlock("p", "paragraph", 0, paragraph, `{}`, `[]`),
		productionValidationBlock("h", "heading", 1, heading, `{"level":3}`, `[]`),
		productionValidationBlock("l", "list", 2, `[`+item+`]`, `{}`, `[]`),
		productionValidationBlock("t", "table", 3, `{"headers":["text"],"rows":[[`+cell+`]]}`, `{}`, `[]`),
		productionValidationBlock("n", "callout", 4, note, `{"kind":"note"}`, `[]`),
		productionValidationBlock("c", "code", 5, `"raw code"`, `{"language":"go"}`, `[]`),
		productionValidationBlock("next", "heading", 6, `"Following"`, `{"level":2}`, `[]`),
	}}

	markdown, err := RenderProductionMarkdown(version)
	require.NoError(t, err)
	require.GreaterOrEqual(t, strings.Count(markdown, "\\`\\`\\`"), 5)
	require.GreaterOrEqual(t, strings.Count(markdown, `\~\~\~`), 5)
	require.GreaterOrEqual(t, strings.Count(markdown, "&gt; quote"), 5)
	require.NotContains(t, markdown, "<br>```")
	require.NotContains(t, markdown, "<br>~~~")
	require.NotContains(t, markdown, "<br>> quote")
	require.Contains(t, markdown, "> **NOTE**<br>")
	require.Contains(t, markdown, "```go\nraw code\n```")
	require.Contains(t, markdown, "\n\n## Following\n")
}

func TestProductionMarkdownRendererEncodesImageMarkdownDelimiters(t *testing.T) {
	version := &types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{
		productionValidationBlock("i", "image", 0, `{"alt":"x","url":"https://example.com/a_(b).png"}`, `{}`, `[]`),
	}}

	markdown, err := RenderProductionMarkdown(version)
	require.NoError(t, err)
	require.Contains(t, markdown, "a_%28b%29.png")
	require.NotContains(t, markdown, "a_(b)")
}

func TestProductionMarkdownRendererRejectsNilUnsupportedAndMalformedBlocks(t *testing.T) {
	_, err := RenderProductionMarkdown(nil)
	require.ErrorContains(t, err, "version")
	_, err = RenderProductionMarkdown(&types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{nil}})
	require.ErrorContains(t, err, "nil block")
	_, err = RenderProductionMarkdown(&types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{
		productionValidationBlock("x", "video", 0, `"x"`, `{}`, `[]`),
	}})
	require.ErrorContains(t, err, "unsupported")
	_, err = RenderProductionMarkdown(&types.ProductionDocumentVersion{Blocks: []*types.ProductionDocumentBlock{
		productionValidationBlock("x", "list", 0, `{"not":"a list"}`, `{}`, `[]`),
	}})
	require.ErrorContains(t, err, "content")
}
