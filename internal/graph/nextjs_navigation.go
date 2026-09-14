package graph

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	gts "github.com/odvcencio/gotreesitter"
)

const maxNavigationOutcomes = 8

type nextRoute struct {
	id          int64
	pattern     string
	kind        string
	file        string
	targetQName string
}

type nextNavigationSite struct {
	file           string
	start          uint32
	end            uint32
	line           int
	operation      string
	evidence       string
	rawDestination string
	values         []nextString
	reason         string
}

type nextString struct {
	text     string
	symbolic bool
}

type nextConsumedRef struct {
	start uint32
	end   uint32
}

type nextNavigationRow struct {
	sourceID       int64
	ordinal        int
	operation      string
	evidence       string
	rawDestination string
	destination    string
	certainty      string
	routeID        int64
	reason         string
	line           int
}

type nextNavigationGraph struct {
	routes      []nextRoute
	navigations []nextNavigationRow
}

type nextExpressionEvaluator struct {
	root  *gts.Node
	lang  *gts.Language
	src   []byte
	stack map[uint32]bool
}

type nextLocalBinding struct {
	kind  string
	value *gts.Node
	start uint32
}

func discoverNextRoutes(
	repo string,
	files []mapperFile,
	syms []mapperSym,
	defaultExportNames map[string]string,
) []nextRoute {
	appRoot := nextAppRoot(repo)
	if appRoot == "" {
		return nil
	}

	byFile := make(map[string][]mapperSym, len(syms))
	for _, sym := range syms {
		if sym.container == "" {
			byFile[sym.row.file] = append(byFile[sym.row.file], sym)
		}
	}

	routes := make([]nextRoute, 0)
	for _, file := range files {
		kind, ok := nextRouteFileKind(path.Base(file.rel))
		if !ok || !strings.HasPrefix(file.rel, appRoot+"/") {
			continue
		}
		dir := strings.TrimSuffix(strings.TrimPrefix(path.Dir(file.rel), appRoot), "/")
		pattern, ok := nextRoutePattern(dir)
		if !ok {
			continue
		}
		routes = append(routes, nextRoute{
			pattern:     pattern,
			kind:        kind,
			file:        file.rel,
			targetQName: nextRouteTargetQName(kind, file.rel, byFile[file.rel], defaultExportNames),
		})
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].pattern != routes[j].pattern {
			return routes[i].pattern < routes[j].pattern
		}
		if routes[i].kind != routes[j].kind {
			return routes[i].kind < routes[j].kind
		}
		return routes[i].file < routes[j].file
	})
	for i := range routes {
		routes[i].id = int64(i + 1)
	}
	return routes
}

func nextAppRoot(repo string) string {
	for _, candidate := range []string{"app", "src/app"} {
		info, err := os.Stat(filepath.Join(repo, filepath.FromSlash(candidate)))
		if err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

func nextRouteFileKind(name string) (string, bool) {
	switch name {
	case "page.js", "page.jsx", "page.ts", "page.tsx":
		return "page", true
	case "route.js", "route.ts":
		return "route", true
	default:
		return "", false
	}
}

func nextRoutePattern(dir string) (string, bool) {
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	public := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "_") || nextInterceptingSegment(part) {
			return "", false
		}
		if strings.HasPrefix(part, "@") || nextRouteGroup(part) {
			continue
		}
		public = append(public, part)
	}
	if len(public) == 0 {
		return "/", true
	}
	return "/" + strings.Join(public, "/"), true
}

func nextRouteGroup(segment string) bool {
	return len(segment) > 2 && segment[0] == '(' && segment[len(segment)-1] == ')' && !nextInterceptingSegment(segment)
}

func nextInterceptingSegment(segment string) bool {
	return strings.HasPrefix(segment, "(.)") || strings.HasPrefix(segment, "(..)") ||
		strings.HasPrefix(segment, "(...)")
}

func nextRouteTargetQName(
	kind string,
	file string,
	syms []mapperSym,
	defaultExportNames map[string]string,
) string {
	if kind == "page" {
		name := defaultExportNames[file]
		for _, sym := range syms {
			if name != "" && sym.row.name == name {
				return sym.row.qname
			}
		}
		return ""
	}

	handlers := make([]string, 0, len(syms))
	for _, sym := range syms {
		if sym.row.exported && slices.Contains(
			[]string{"GET", "HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"},
			sym.row.name,
		) {
			handlers = append(handlers, sym.row.qname)
		}
	}
	if len(handlers) == 1 {
		return handlers[0]
	}
	return ""
}

func nextNavigationFromMapperTree(
	f mapperFile,
	src []byte,
	tree *gts.Tree,
	lang *gts.Language,
	bindings map[string]mapperImportBinding,
) ([]nextNavigationSite, []nextConsumedRef, string) {
	root := tree.RootNode()
	if root == nil || lang == nil {
		return nil, nil, ""
	}
	evaluator := nextExpressionEvaluator{
		root:  root,
		lang:  lang,
		src:   src,
		stack: map[uint32]bool{},
	}

	var sites []nextNavigationSite
	var consumed []nextConsumedRef
	stack := []*gts.Node{root}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch node.Type(lang) {
		case "jsx_opening_element", "jsx_self_closing_element":
			nameNode := node.ChildByFieldName("name", lang)
			if nameNode != nil && nextDefaultImportVisible(node, nameNode.Text(src), "next/link", bindings, &evaluator) {
				consumed = append(consumed, nextConsumedRef{start: node.StartByte(), end: node.EndByte()})
				if href := nextJSXAttribute(node, "href", lang, src); href != nil {
					values, reason := evaluator.destination(href, true, false)
					sites = append(sites, nextNavigationSite{
						file:           f.rel,
						start:          node.StartByte(),
						end:            node.EndByte(),
						line:           int(node.StartPoint().Row) + 1,
						operation:      "link",
						evidence:       "inferred",
						rawDestination: strings.TrimSpace(href.Text(src)),
						values:         values,
						reason:         reason,
					})
				}
			}
		case "call_expression":
			if site, consume := nextCallSite(f.rel, node, bindings, &evaluator); consume {
				consumed = append(consumed, nextConsumedRef{start: node.StartByte(), end: node.EndByte()})
				if site != nil {
					sites = append(sites, *site)
				}
			}
		}
		for i := node.ChildCount() - 1; i >= 0; i-- {
			if child := node.Child(i); child != nil {
				stack = append(stack, child)
			}
		}
	}
	return sites, consumed, nextDefaultExportName(root, lang, src)
}

func nextCallSite(
	file string,
	call *gts.Node,
	bindings map[string]mapperImportBinding,
	evaluator *nextExpressionEvaluator,
) (*nextNavigationSite, bool) {
	fn := call.ChildByFieldName("function", evaluator.lang)
	if fn == nil {
		return nil, false
	}
	receiver, name := nextCallee(fn, evaluator.lang, evaluator.src)
	operation := ""
	if receiver == "" {
		switch name {
		case "redirect", "permanentRedirect":
			if nextNamedImportVisible(call, name, name, "next/navigation", bindings, evaluator) {
				operation = name
			}
		case "useRouter":
			if nextNamedImportVisible(call, name, "useRouter", "next/navigation", bindings, evaluator) {
				return nil, true
			}
		default:
			binding := bindings[name]
			if binding.specifier == "next/navigation" && !binding.namespace &&
				nextImportVisible(call, name, evaluator) {
				switch binding.imported {
				case "redirect", "permanentRedirect":
					operation = binding.imported
				case "useRouter":
					return nil, true
				}
			}
		}
	} else if nextNamespaceImportVisible(call, receiver, "next/navigation", bindings, evaluator) {
		switch name {
		case "redirect", "permanentRedirect":
			operation = name
		case "useRouter":
			return nil, true
		}
	} else if nextRouterReceiver(call, receiver, bindings, evaluator) {
		switch name {
		case "push", "replace":
			operation = name
		case "prefetch", "back", "forward", "refresh":
			return nil, true
		}
	}
	if operation == "" {
		return nil, false
	}

	arg := nextFirstArgument(call, evaluator.lang)
	values, reason := evaluator.destination(arg, false, false)
	raw := ""
	if arg != nil {
		raw = strings.TrimSpace(arg.Text(evaluator.src))
	}
	return &nextNavigationSite{
		file:           file,
		start:          call.StartByte(),
		end:            call.EndByte(),
		line:           int(call.StartPoint().Row) + 1,
		operation:      operation,
		evidence:       "direct",
		rawDestination: raw,
		values:         values,
		reason:         reason,
	}, true
}

func nextCallee(fn *gts.Node, lang *gts.Language, src []byte) (string, string) {
	switch fn.Type(lang) {
	case "identifier":
		return "", fn.Text(src)
	case "member_expression":
		object := fn.ChildByFieldName("object", lang)
		property := fn.ChildByFieldName("property", lang)
		if object != nil && property != nil && property.Type(lang) == "property_identifier" {
			return object.Text(src), property.Text(src)
		}
	}
	return "", ""
}

func nextFirstArgument(call *gts.Node, lang *gts.Language) *gts.Node {
	args := call.ChildByFieldName("arguments", lang)
	if args == nil || args.NamedChildCount() == 0 {
		return nil
	}
	return args.NamedChild(0)
}

func nextJSXAttribute(node *gts.Node, name string, lang *gts.Language, src []byte) *gts.Node {
	for i := range node.NamedChildCount() {
		child := node.NamedChild(i)
		if child == nil || child.Type(lang) != "jsx_attribute" {
			continue
		}
		key := child.ChildByFieldName("name", lang)
		if key == nil && child.NamedChildCount() > 0 {
			key = child.NamedChild(0)
		}
		if key == nil || key.Text(src) != name {
			continue
		}
		value := child.ChildByFieldName("value", lang)
		if value == nil && child.NamedChildCount() > 1 {
			value = child.NamedChild(1)
		}
		if value == nil {
			return nil
		}
		if value.Type(lang) == "jsx_expression" {
			return nextFirstNamed(value)
		}
		return value
	}
	return nil
}

func nextDefaultImportVisible(
	site *gts.Node,
	name string,
	specifier string,
	bindings map[string]mapperImportBinding,
	evaluator *nextExpressionEvaluator,
) bool {
	binding, ok := bindings[name]
	return ok && binding.specifier == specifier && binding.defaultImport &&
		nextImportVisible(site, name, evaluator)
}

func nextNamedImportVisible(
	site *gts.Node,
	name string,
	imported string,
	specifier string,
	bindings map[string]mapperImportBinding,
	evaluator *nextExpressionEvaluator,
) bool {
	binding, ok := bindings[name]
	return ok && binding.specifier == specifier && binding.imported == imported &&
		!binding.namespace && nextImportVisible(site, name, evaluator)
}

func nextNamespaceImportVisible(
	site *gts.Node,
	name string,
	specifier string,
	bindings map[string]mapperImportBinding,
	evaluator *nextExpressionEvaluator,
) bool {
	binding, ok := bindings[name]
	return ok && binding.specifier == specifier && binding.namespace &&
		nextImportVisible(site, name, evaluator)
}

func nextImportVisible(site *gts.Node, name string, evaluator *nextExpressionEvaluator) bool {
	_, found := evaluator.localBinding(site, name)
	return !found
}

func nextRouterReceiver(
	site *gts.Node,
	receiver string,
	bindings map[string]mapperImportBinding,
	evaluator *nextExpressionEvaluator,
) bool {
	if receiver == "" {
		return false
	}
	binding, ok := evaluator.localBinding(site, receiver)
	if !ok || binding.kind != "const" || binding.value == nil || binding.start >= site.StartByte() {
		return false
	}
	init := nextUnwrapExpression(binding.value, evaluator.lang)
	if init == nil || init.Type(evaluator.lang) != "call_expression" {
		return false
	}
	fn := init.ChildByFieldName("function", evaluator.lang)
	if fn == nil {
		return false
	}
	importReceiver, name := nextCallee(fn, evaluator.lang, evaluator.src)
	if importReceiver == "" {
		return nextNamedImportVisible(init, name, "useRouter", "next/navigation", bindings, evaluator)
	}
	return name == "useRouter" && nextNamespaceImportVisible(
		init,
		importReceiver,
		"next/navigation",
		bindings,
		evaluator,
	)
}

func (e *nextExpressionEvaluator) destination(
	node *gts.Node,
	allowObject bool,
	allowSymbol bool,
) ([]nextString, string) {
	if node == nil {
		return nil, "unsupported_expression"
	}
	node = nextUnwrapExpression(node, e.lang)
	if node == nil {
		return nil, "unsupported_expression"
	}

	switch node.Type(e.lang) {
	case "string":
		value, ok := nextStringLiteral(node.Text(e.src))
		if !ok {
			return nil, "unsupported_expression"
		}
		return []nextString{{text: value}}, ""
	case "template_string":
		return e.template(node)
	case "identifier":
		name := node.Text(e.src)
		// Anything but an evaluable earlier const is a runtime value: symbolic
		// inside a template, non_literal as a whole destination.
		binding, ok := e.localBinding(node, name)
		if !ok || binding.kind != "const" || binding.value == nil || binding.start >= node.StartByte() {
			if allowSymbol {
				return []nextString{{text: "${" + name + "}", symbolic: true}}, ""
			}
			return nil, "non_literal"
		}
		if e.stack[binding.start] {
			return nil, "unsupported_expression"
		}
		e.stack[binding.start] = true
		values, reason := e.destination(binding.value, allowObject, allowSymbol)
		delete(e.stack, binding.start)
		return values, reason
	case "binary_expression":
		return e.concatenation(node)
	case "ternary_expression", "conditional_expression":
		return e.conditional(node, allowObject, allowSymbol)
	case "object":
		if allowObject {
			return e.hrefObject(node)
		}
	}
	return nil, "unsupported_expression"
}

func (e *nextExpressionEvaluator) concatenation(node *gts.Node) ([]nextString, string) {
	left := node.ChildByFieldName("left", e.lang)
	right := node.ChildByFieldName("right", e.lang)
	if left == nil || right == nil || strings.TrimSpace(string(e.src[left.EndByte():right.StartByte()])) != "+" {
		return nil, "unsupported_expression"
	}
	lvals, reason := e.destination(left, false, true)
	if reason != "" {
		return nil, reason
	}
	rvals, reason := e.destination(right, false, true)
	if reason != "" {
		return nil, reason
	}
	return nextProduct(lvals, rvals)
}

func (e *nextExpressionEvaluator) conditional(
	node *gts.Node,
	allowObject bool,
	allowSymbol bool,
) ([]nextString, string) {
	consequence := node.ChildByFieldName("consequence", e.lang)
	alternative := node.ChildByFieldName("alternative", e.lang)
	if consequence == nil || alternative == nil {
		return nil, "unsupported_expression"
	}
	left, reason := e.destination(consequence, allowObject, allowSymbol)
	if reason != "" {
		return nil, reason
	}
	right, reason := e.destination(alternative, allowObject, allowSymbol)
	if reason != "" {
		return nil, reason
	}
	return nextUnion(left, right)
}

func (e *nextExpressionEvaluator) template(node *gts.Node) ([]nextString, string) {
	values := []nextString{{}}
	for i := range node.NamedChildCount() {
		child := node.NamedChild(i)
		if child == nil {
			continue
		}
		var additions []nextString
		switch child.Type(e.lang) {
		case "string_fragment":
			additions = []nextString{{text: child.Text(e.src)}}
		case "template_substitution":
			expression := nextFirstNamed(child)
			var reason string
			additions, reason = e.destination(expression, false, true)
			if reason != "" {
				return nil, reason
			}
		default:
			continue
		}
		var reason string
		values, reason = nextProduct(values, additions)
		if reason != "" {
			return nil, reason
		}
	}
	return values, ""
}

func (e *nextExpressionEvaluator) hrefObject(node *gts.Node) ([]nextString, string) {
	var pathname *gts.Node
	for i := range node.NamedChildCount() {
		child := node.NamedChild(i)
		if child == nil {
			continue
		}
		if child.Type(e.lang) == "spread_element" {
			return nil, "unsupported_expression"
		}
		if child.Type(e.lang) != "pair" {
			continue
		}
		key := child.ChildByFieldName("key", e.lang)
		value := child.ChildByFieldName("value", e.lang)
		if key == nil || value == nil {
			continue
		}
		keyText := strings.Trim(strings.TrimSpace(key.Text(e.src)), "'\"")
		if keyText != "pathname" {
			continue
		}
		if pathname != nil {
			return nil, "unsupported_expression"
		}
		pathname = value
	}
	if pathname == nil {
		return nil, "unsupported_expression"
	}
	return e.destination(pathname, false, false)
}

func nextProduct(left, right []nextString) ([]nextString, string) {
	if len(left) == 0 || len(right) == 0 || len(left)*len(right) > maxNavigationOutcomes {
		return nil, "unsupported_expression"
	}
	out := make([]nextString, 0, len(left)*len(right))
	for _, a := range left {
		for _, b := range right {
			out = append(out, nextString{
				text:     a.text + b.text,
				symbolic: a.symbolic || b.symbolic,
			})
		}
	}
	return nextUniqueStrings(out)
}

func nextUnion(left, right []nextString) ([]nextString, string) {
	if len(left)+len(right) > maxNavigationOutcomes {
		return nil, "unsupported_expression"
	}
	return nextUniqueStrings(append(append([]nextString{}, left...), right...))
}

func nextUniqueStrings(values []nextString) ([]nextString, string) {
	seen := map[nextString]bool{}
	out := make([]nextString, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
		if len(out) > maxNavigationOutcomes {
			return nil, "unsupported_expression"
		}
	}
	return out, ""
}

func nextStringLiteral(raw string) (string, bool) {
	if len(raw) < 2 {
		return "", false
	}
	quote := raw[0]
	if (quote != '\'' && quote != '"') || raw[len(raw)-1] != quote {
		return "", false
	}
	body := raw[1 : len(raw)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			b.WriteByte(body[i])
			continue
		}
		i++
		if i >= len(body) {
			return "", false
		}
		switch body[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case '\n':
			continue
		default:
			b.WriteByte(body[i])
		}
	}
	return b.String(), true
}

func nextUnwrapExpression(node *gts.Node, lang *gts.Language) *gts.Node {
	for node != nil {
		switch node.Type(lang) {
		case "parenthesized_expression", "as_expression", "satisfies_expression", "type_assertion", "non_null_expression":
			if expression := node.ChildByFieldName("expression", lang); expression != nil {
				node = expression
				continue
			}
			if child := nextFirstNamed(node); child != nil {
				node = child
				continue
			}
		case "jsx_expression":
			node = nextFirstNamed(node)
			continue
		}
		return node
	}
	return nil
}

func nextFirstNamed(node *gts.Node) *gts.Node {
	if node == nil || node.NamedChildCount() == 0 {
		return nil
	}
	return node.NamedChild(0)
}

func (e *nextExpressionEvaluator) localBinding(site *gts.Node, name string) (nextLocalBinding, bool) {
	for node := site; node != nil; node = node.Parent() {
		if !nextLexicalScope(node.Type(e.lang)) {
			continue
		}
		if binding, ok := e.bindingInScope(node, name); ok {
			return binding, true
		}
	}
	return nextLocalBinding{}, false
}

func (e *nextExpressionEvaluator) bindingInScope(scope *gts.Node, name string) (nextLocalBinding, bool) {
	var found nextLocalBinding
	var walk func(*gts.Node)
	walk = func(node *gts.Node) {
		if node == nil || found.kind != "" {
			return
		}
		typeName := node.Type(e.lang)
		if typeName == "variable_declarator" {
			// A destructuring pattern binds too; only a plain identifier
			// takes the initializer as its value, so a pattern stays mutable.
			bindingName := node.ChildByFieldName("name", e.lang)
			if nextPatternBinds(bindingName, name, e.lang, e.src) {
				found = nextLocalBinding{kind: "mutable", start: node.StartByte()}
				if bindingName.Type(e.lang) == "identifier" {
					if parent := node.Parent(); parent != nil && parent.Type(e.lang) == "lexical_declaration" &&
						strings.HasPrefix(strings.TrimSpace(parent.Text(e.src)), "const ") {
						found.kind = "const"
						found.value = node.ChildByFieldName("value", e.lang)
					}
				}
				return
			}
		}
		if slices.Contains([]string{"function_declaration", "class_declaration"}, typeName) {
			bindingName := node.ChildByFieldName("name", e.lang)
			if bindingName != nil && bindingName.Text(e.src) == name {
				found = nextLocalBinding{kind: "mutable", start: node.StartByte()}
				return
			}
		}
		// Scope-introduced bindings: function "parameters", a single-param
		// arrow or catch clause "parameter", and a for-in/of "left".
		if node == scope {
			for _, field := range []string{"parameters", "parameter", "left"} {
				if p := node.ChildByFieldName(field, e.lang); nextPatternBinds(p, name, e.lang, e.src) {
					found = nextLocalBinding{kind: "mutable", start: p.StartByte()}
					return
				}
			}
		}
		if node != scope && nextLexicalScope(typeName) {
			return
		}
		for i := range node.ChildCount() {
			walk(node.Child(i))
		}
	}
	walk(scope)
	return found, found.kind != ""
}

func nextLexicalScope(nodeType string) bool {
	return nodeType == "program" || nodeType == "statement_block" ||
		nodeType == "class_body" || nodeType == "catch_clause" ||
		nodeType == "for_statement" || nodeType == "for_in_statement" ||
		nodeType == "for_of_statement" || nodeType == "switch_statement" ||
		nextFunctionScope(nodeType)
}

func nextFunctionScope(nodeType string) bool {
	return nodeType == "function_declaration" || nodeType == "function_expression" ||
		nodeType == "arrow_function" || nodeType == "method_definition" ||
		nodeType == "generator_function" || nodeType == "generator_function_declaration"
}

func nextPatternBinds(node *gts.Node, name string, lang *gts.Language, src []byte) bool {
	if node == nil {
		return false
	}
	if node.Type(lang) == "type_annotation" {
		return false
	}
	if t := node.Type(lang); (t == "identifier" || t == "shorthand_property_identifier_pattern") && node.Text(src) == name {
		return true
	}
	for i := range node.NamedChildCount() {
		if nextPatternBinds(node.NamedChild(i), name, lang, src) {
			return true
		}
	}
	return false
}

func nextDefaultExportName(root *gts.Node, lang *gts.Language, src []byte) string {
	for i := range root.NamedChildCount() {
		node := root.NamedChild(i)
		if node == nil || node.Type(lang) != "export_statement" ||
			!strings.HasPrefix(strings.TrimSpace(node.Text(src)), "export default") {
			continue
		}
		declaration := node.ChildByFieldName("declaration", lang)
		if declaration == nil {
			continue
		}
		name := declaration.ChildByFieldName("name", lang)
		if name != nil {
			return name.Text(src)
		}
	}
	return ""
}

func filterNextConsumedCalls(refs []gts.CallRef, consumed []nextConsumedRef) []gts.CallRef {
	if len(consumed) == 0 {
		return refs
	}
	out := make([]gts.CallRef, 0, len(refs))
	for _, ref := range refs {
		matched := false
		for _, span := range consumed {
			if ref.StartByte == span.start && ref.EndByte == span.end {
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, ref)
		}
	}
	return out
}

func nextFreshMapperSymbols(syms []mapperSym, hasError map[string]bool) []mapperSym {
	out := make([]mapperSym, 0, len(syms))
	for _, sym := range syms {
		if !hasError[sym.row.file] {
			out = append(out, sym)
		}
	}
	return out
}

func buildNextNavigationGraph(
	repo string,
	files []mapperFile,
	syms []mapperSym,
	sites []nextNavigationSite,
	defaultExportNames map[string]string,
) nextNavigationGraph {
	routes := discoverNextRoutes(repo, files, syms, defaultExportNames)
	byFile := map[string][]mapperSym{}
	for _, sym := range syms {
		byFile[sym.row.file] = append(byFile[sym.row.file], sym)
	}
	for file := range byFile {
		sort.Slice(byFile[file], func(i, j int) bool { return byFile[file][i].start < byFile[file][j].start })
	}
	origins := map[string]string{}
	for _, route := range routes {
		origins[route.file] = route.pattern
	}

	rows := make([]nextNavigationRow, 0, len(sites))
	ordinal := 0
	for _, site := range sites {
		caller, ok := innermostContainer(byFile[site.file], site.start, site.end)
		if !ok {
			continue
		}
		if site.reason != "" || len(site.values) == 0 {
			rows = append(rows, nextNavigationRow{
				sourceID:       caller.row.id,
				ordinal:        ordinal,
				operation:      site.operation,
				evidence:       site.evidence,
				rawDestination: site.rawDestination,
				certainty:      "unresolved",
				reason:         nextReason(site.reason, "unsupported_expression"),
				line:           site.line,
			})
			ordinal++
			continue
		}

		seen := map[string]bool{}
		for _, value := range site.values {
			row := resolveNextDestination(site, caller.row.id, ordinal, value, origins[site.file], routes)
			key := strings.Join(
				[]string{row.destination, row.certainty, row.reason, strconv.FormatInt(row.routeID, 10)},
				"\x00",
			)
			if seen[key] {
				continue
			}
			seen[key] = true
			rows = append(rows, row)
			ordinal++
		}
	}
	return nextNavigationGraph{routes: routes, navigations: rows}
}

func nextReason(reason, fallback string) string {
	if reason != "" {
		return reason
	}
	return fallback
}

func resolveNextDestination(
	site nextNavigationSite,
	sourceID int64,
	ordinal int,
	value nextString,
	origin string,
	routes []nextRoute,
) nextNavigationRow {
	row := nextNavigationRow{
		sourceID:       sourceID,
		ordinal:        ordinal,
		operation:      site.operation,
		evidence:       site.evidence,
		rawDestination: site.rawDestination,
		destination:    value.text,
		certainty:      "unresolved",
		line:           site.line,
	}
	matchPath, reason := nextLocalPath(value.text, origin)
	if reason != "" {
		row.reason = reason
		return row
	}
	matches := nextMatchingRoutes(matchPath, value.symbolic, routes)
	if len(matches) == 0 {
		row.reason = "no_matching_route"
		return row
	}
	if len(matches) > 1 {
		row.reason = "ambiguous_route"
		return row
	}
	row.routeID = matches[0].route.id
	row.certainty = "matched"
	if matches[0].conditional {
		row.certainty = "conditional"
	}
	return row
}

func nextLocalPath(destination, origin string) (string, string) {
	if destination == "" || strings.HasPrefix(destination, "?") || strings.HasPrefix(destination, "#") {
		return "", "same_route"
	}
	if strings.HasPrefix(destination, "//") {
		return "", "external"
	}
	if parsed, err := url.Parse(destination); err == nil && parsed.Scheme != "" {
		return "", "external"
	}

	matchPath := destination
	if i := strings.IndexAny(matchPath, "?#"); i >= 0 {
		matchPath = matchPath[:i]
	}
	if !strings.HasPrefix(matchPath, "/") {
		if origin == "" {
			return "", "relative_without_origin"
		}
		matchPath = path.Join(path.Dir(origin), matchPath)
	}
	matchPath = path.Clean("/" + strings.TrimPrefix(matchPath, "/"))
	return matchPath, ""
}

type nextRouteMatch struct {
	route       nextRoute
	conditional bool
	score       int
}

func nextMatchingRoutes(destination string, symbolic bool, routes []nextRoute) []nextRouteMatch {
	var matches []nextRouteMatch
	best := -1
	for _, route := range routes {
		conditional, score, ok := nextRouteMatches(route.pattern, destination, symbolic)
		if !ok || score < best {
			continue
		}
		if score > best {
			matches = nil
			best = score
		}
		matches = append(matches, nextRouteMatch{route: route, conditional: conditional, score: score})
	}
	return matches
}

func nextRouteMatches(pattern, destination string, symbolic bool) (bool, int, bool) {
	patterns := nextPathSegments(pattern)
	destinations := nextPathSegments(destination)
	conditional, ok := nextMatchSegments(patterns, destinations, symbolic)
	if !ok {
		return false, 0, false
	}
	score := 0
	for _, segment := range patterns {
		switch nextSegmentKind(segment) {
		case "static":
			score += 1000
		case "dynamic":
			score += 100
		case "catchall":
			score += 10
		case "optional":
			score++
		}
	}
	return conditional, score, true
}

func nextMatchSegments(patterns, destinations []string, symbolic bool) (bool, bool) {
	if len(patterns) == 0 {
		return false, len(destinations) == 0
	}
	segment := patterns[0]
	switch nextSegmentKind(segment) {
	case "optional":
		for consumed := 0; consumed <= len(destinations); consumed++ {
			conditional, ok := nextMatchSegments(patterns[1:], destinations[consumed:], symbolic)
			if ok {
				return conditional || symbolic && nextAnySymbolic(destinations[:consumed]), true
			}
		}
		return false, false
	case "catchall":
		for consumed := 1; consumed <= len(destinations); consumed++ {
			conditional, ok := nextMatchSegments(patterns[1:], destinations[consumed:], symbolic)
			if ok {
				return conditional || symbolic && nextAnySymbolic(destinations[:consumed]), true
			}
		}
		return false, false
	case "dynamic":
		if len(destinations) == 0 || destinations[0] == "" {
			return false, false
		}
		conditional, ok := nextMatchSegments(patterns[1:], destinations[1:], symbolic)
		return conditional || symbolic && nextSymbolicSegment(destinations[0]), ok
	default:
		if len(destinations) == 0 || destinations[0] != segment || symbolic && nextSymbolicSegment(destinations[0]) {
			return false, false
		}
		return nextMatchSegments(patterns[1:], destinations[1:], symbolic)
	}
}

func nextPathSegments(value string) []string {
	value = strings.Trim(value, "/")
	if value == "" {
		return nil
	}
	return strings.Split(value, "/")
}

func nextSegmentKind(segment string) string {
	switch {
	case strings.HasPrefix(segment, "[[...") && strings.HasSuffix(segment, "]]"):
		return "optional"
	case strings.HasPrefix(segment, "[...") && strings.HasSuffix(segment, "]"):
		return "catchall"
	case strings.HasPrefix(segment, "[") && strings.HasSuffix(segment, "]"):
		return "dynamic"
	default:
		return "static"
	}
}

func nextAnySymbolic(segments []string) bool {
	for _, segment := range segments {
		if nextSymbolicSegment(segment) {
			return true
		}
	}
	return false
}

func nextSymbolicSegment(segment string) bool {
	return strings.Contains(segment, "${") && strings.Contains(segment, "}")
}

func navigationRows(
	ctx context.Context,
	conn *sql.DB,
	sourceID int64,
) ([]NavigationDestination, []UnresolvedNavigationDestination, bool, error) {
	var exists int
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'navigations'
	)`).Scan(&exists); err != nil {
		return nil, nil, false, err
	}
	if exists == 0 {
		return nil, nil, false, nil
	}

	rows, err := conn.QueryContext(ctx, `SELECT
		n.operation, n.evidence, n.raw_destination, n.destination, n.certainty,
		COALESCE(r.pattern, ''), COALESCE(r.file, ''), COALESCE(r.target_qname, ''),
		s.file, n.line, n.reason
		FROM navigations n
		JOIN symbols s ON s.id = n.source_symbol
		LEFT JOIN routes r ON r.id = n.route_id
		WHERE n.source_symbol = ?
		ORDER BY n.ordinal
		LIMIT ?`, sourceID, maxNavigationResults+1)
	if err != nil {
		return nil, nil, false, err
	}
	defer rows.Close()

	resolved := make([]NavigationDestination, 0)
	unresolved := make([]UnresolvedNavigationDestination, 0)
	count := 0
	truncated := false
	for rows.Next() {
		var operation, evidence, raw, destination, certainty string
		var route, targetFile, targetQName, file, reason string
		var line int
		if err := rows.Scan(
			&operation,
			&evidence,
			&raw,
			&destination,
			&certainty,
			&route,
			&targetFile,
			&targetQName,
			&file,
			&line,
			&reason,
		); err != nil {
			return nil, nil, false, err
		}
		if count >= maxNavigationResults {
			truncated = true
			continue
		}
		count++
		if certainty == "unresolved" {
			unresolved = append(unresolved, UnresolvedNavigationDestination{
				Operation:      operation,
				Evidence:       evidence,
				RawDestination: raw,
				Destination:    destination,
				Reason:         reason,
				File:           file,
				Line:           line,
			})
			continue
		}
		resolved = append(resolved, NavigationDestination{
			Operation:      operation,
			Evidence:       evidence,
			Certainty:      certainty,
			RawDestination: raw,
			Destination:    destination,
			Route:          route,
			TargetFile:     targetFile,
			TargetQName:    targetQName,
			File:           file,
			Line:           line,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, err
	}
	return resolved, unresolved, truncated, nil
}

func writeNextNavigation(
	ctx context.Context,
	tx *sql.Tx,
	graph nextNavigationGraph,
) error {
	routeInsert, err := tx.PrepareContext(ctx, `INSERT INTO routes
		(id, pattern, kind, file, target_qname) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer routeInsert.Close()
	for _, route := range graph.routes {
		if _, err := routeInsert.ExecContext(
			ctx,
			route.id,
			route.pattern,
			route.kind,
			route.file,
			route.targetQName,
		); err != nil {
			return err
		}
	}

	navigationInsert, err := tx.PrepareContext(ctx, `INSERT INTO navigations
		(source_symbol, ordinal, operation, evidence, raw_destination, destination,
		 certainty, route_id, reason, line)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer navigationInsert.Close()
	for _, navigation := range graph.navigations {
		var routeID any
		if navigation.routeID != 0 {
			routeID = navigation.routeID
		}
		if _, err := navigationInsert.ExecContext(
			ctx,
			navigation.sourceID,
			navigation.ordinal,
			navigation.operation,
			navigation.evidence,
			navigation.rawDestination,
			navigation.destination,
			navigation.certainty,
			routeID,
			navigation.reason,
			navigation.line,
		); err != nil {
			return err
		}
	}
	return nil
}
