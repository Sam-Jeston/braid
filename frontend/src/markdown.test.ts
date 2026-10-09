import { expect, test } from "bun:test";
import { renderMarkdown } from "./markdown";

// Agent output and teammates' messages are untrusted: nothing executable may survive.
test("renders ordinary markdown", () => {
  const html = renderMarkdown("# Brief\n\n- **bold** and `code`\n\n[docs](https://example.com)");
  expect(html).toContain("<h1>Brief</h1>");
  expect(html).toContain("<strong>bold</strong>");
  expect(html).toContain('<a href="https://example.com" rel="noopener noreferrer">docs</a>');
});

test.each([
  ["script tags", "<script>alert(1)</script>", "<script"],
  ["event handlers", '<img src="x" onerror="alert(1)">', "onerror"],
  ["javascript: links", "[click](javascript:alert(1))", "javascript:"],
  ["iframes", '<iframe src="https://evil.example"></iframe>', "<iframe"],
  ["forms", '<form action="https://evil.example"><input name="pw"></form>', "<form"],
  ["inline styles", '<p style="position:fixed;inset:0">overlay</p>', "style="],
  ["style tags", "<style>body{display:none}</style>", "<style"],
  ["svg handlers", '<svg><a xlink:href="javascript:alert(1)"><text>x</text></a></svg>', "javascript:"],
])("strips %s", (_, input, forbidden) => {
  expect(renderMarkdown(input).toLowerCase()).not.toContain(forbidden);
});
