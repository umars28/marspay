package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	hookInHTML = regexp.MustCompile(`data-live="([a-z0-9-]+)"`)
	hookInJS   = regexp.MustCompile(`(?:set|restore|remember|slot|note|clearNote)\(\s*'([a-z0-9-]+)'`)
	hookInList = regexp.MustCompile(`'([a-z0-9-]+)',`)
	idInHTML   = regexp.MustCompile(`\bid="([A-Za-z0-9_-]+)"`)
	idInJS     = regexp.MustCompile(`(?:\$|querySelector)\(\s*'#([A-Za-z0-9_-]+)'`)
	screenRef  = regexp.MustCompile(`data-(?:screen|goto|pscreen)="([a-z0-9-]+)"`)
	screenDecl = regexp.MustCompile(`class="(?:screen|pscreen)[^"]*" data-(?:screen|pscreen)="([a-z0-9-]+)"`)
	slotLists  = regexp.MustCompile(`(?s)var (?:MERCHANT_SLOTS|OPS_SLOTS) = \[(.*?)\];`)
)

var problems int

func report(kind, format string, args ...any) {
	problems++
	fmt.Printf("%-5s %s\n", kind, fmt.Sprintf(format, args...))
}

func read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "uilint:", err)
		os.Exit(1)
	}
	return string(b)
}

func collect(re *regexp.Regexp, text string) map[string]bool {
	out := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		out[m[1]] = true
	}
	return out
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func main() {
	dir := "mockup"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	html := read(filepath.Join(dir, "index.html"))
	css := read(filepath.Join(dir, "app.css"))

	var js strings.Builder
	for _, name := range []string{"api.js", "app.js", "live.js", "dashboards.js"} {
		js.WriteString(read(filepath.Join(dir, name)))
		js.WriteString("\n")
	}
	script := js.String()

	fmt.Println("==> checking the mockup against itself")

	if strings.Contains(html, " hidden>") || strings.Contains(html, ` hidden `) {
		if !strings.Contains(css, "[hidden]") {
			report("FAIL", "the page uses the hidden attribute but app.css has no [hidden] rule; "+
				"any class that sets display will keep hidden elements visible")
		}
	}

	htmlHooks := collect(hookInHTML, html)
	jsHooks := collect(hookInJS, script)
	for _, m := range slotLists.FindAllStringSubmatch(script, -1) {
		for k := range collect(hookInList, m[1]) {
			jsHooks[k] = true
		}
	}

	for _, hook := range sorted(jsHooks) {
		if !htmlHooks[hook] {
			report("FAIL", "the scripts write to data-live=%q but no element carries it", hook)
		}
	}

	unused := []string{}
	for _, hook := range sorted(htmlHooks) {
		if !jsHooks[hook] {
			unused = append(unused, hook)
		}
	}

	htmlIDs := collect(idInHTML, html)
	for _, id := range sorted(collect(idInJS, script)) {
		if !htmlIDs[id] {
			report("FAIL", "the scripts look up #%s but the page has no such id", id)
		}
	}

	declared := collect(screenDecl, html)
	for _, target := range sorted(collect(screenRef, html)) {
		if !declared[target] {
			report("FAIL", "a control navigates to %q but no screen declares it", target)
		}
	}

	fmt.Printf("     %d data-live hooks, %d written by the scripts, %d still sample only\n",
		len(htmlHooks), len(jsHooks), len(unused))
	fmt.Printf("     %d screens declared\n", len(declared))

	if problems == 0 {
		fmt.Println("\nok   every hook, id and navigation target resolves")
		return
	}
	fmt.Printf("\n%d problems\n", problems)
	os.Exit(1)
}
