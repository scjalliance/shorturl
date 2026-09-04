// Command pathaudit walks every link in Firestore and reports which path
// rules would not behave the same under Go's RE2 regexp engine, plus counts
// of each link mode. Run it before cutover and again before deleting the
// Cloud Function.
//
//	go run ./cmd/pathaudit -project <project-id>
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
)

func main() {
	project := flag.String("project", os.Getenv("GOOGLE_CLOUD_PROJECT"), "GCP project ID")
	flag.Parse()
	if *project == "" {
		fmt.Fprintln(os.Stderr, "pathaudit: -project is required")
		os.Exit(2)
	}
	if err := run(context.Background(), *project); err != nil {
		fmt.Fprintln(os.Stderr, "pathaudit:", err)
		os.Exit(1)
	}
}

// run prints one line per problem and a summary of link modes.
func run(ctx context.Context, project string) error {
	client, err := firestore.NewClient(ctx, project)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer client.Close()

	var links, passthrough, usePaths, frame, rules, problems, startsWorking int
	cols := client.Collections(ctx)
	for {
		col, err := cols.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("listing collections: %w", err)
		}
		docs := col.Documents(ctx)
		for {
			doc, err := docs.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return fmt.Errorf("listing %s: %w", col.ID, err)
			}
			links++
			data := doc.Data()
			if truthy(data["passthrough"]) {
				passthrough++
			}
			if truthy(data["frame"]) {
				frame++
			}
			if !truthy(data["usePaths"]) {
				continue
			}
			usePaths++
			paths, err := doc.Ref.Collection("paths").Documents(ctx).GetAll()
			if err != nil {
				return fmt.Errorf("listing %s/%s/paths: %w", col.ID, doc.Ref.ID, err)
			}
			for _, p := range paths {
				rules++
				pattern, isString := p.Data()["pattern"].(string)
				if !isString || pattern == "" {
					problems++
					fmt.Printf("MISSING PATTERN   %s/%s/paths/%s  %v\n", col.ID, doc.Ref.ID, p.Ref.ID, p.Data()["pattern"])
					continue
				}
				re, err := regexp.Compile(pattern)
				if err != nil {
					problems++
					fmt.Printf("DOES NOT COMPILE  %s/%s/paths/%s  %q  %v\n", col.ID, doc.Ref.ID, p.Ref.ID, pattern, err)
					continue
				}
				named := false
				for _, n := range re.SubexpNames()[1:] {
					if n != "" {
						named = true
					}
				}
				if !named {
					startsWorking++
					fmt.Printf("STARTS WORKING    %s/%s/paths/%s  %q  (the function never matched rules without a named group; the port does)\n", col.ID, doc.Ref.ID, p.Ref.ID, pattern)
				}
			}
		}
	}
	fmt.Printf("\nlinks=%d passthrough=%d frame=%d usePaths=%d pathRules=%d problems=%d startsWorking=%d\n", links, passthrough, frame, usePaths, rules, problems, startsWorking)
	return nil
}

// truthy mirrors JavaScript truthiness for the scalar types Firestore returns.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case int64:
		return x != 0
	case float64:
		return x != 0
	default:
		return true
	}
}
