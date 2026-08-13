package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"Pantegnos/modules"

	_ "Pantegnos/modules/impl"
)

func main() {
	inputDir := flag.String("input", "configs", "Input directory containing config files")
	outputDir := flag.String("output", "output", "Directory to save decrypted files")
	flag.Parse()

	if err := os.MkdirAll(*inputDir, 0755); err != nil {
		fmt.Fprintln(os.Stderr, "error creating input directory:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		fmt.Fprintln(os.Stderr, "error creating output directory:", err)
		os.Exit(1)
	}

	entries, err := os.ReadDir(*inputDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading directory %s: %v\n", *inputDir, err)
		os.Exit(1)
	}

	processed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(*inputDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading %s: %v\n", path, err)
			continue
		}

		fmt.Printf("Decrypting: %s\n", path)
		out, err := modules.Process(entry.Name(), data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [!] %s\n", err)
			continue
		}

		outPath := filepath.Join(*outputDir, strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))+".txt")
		if err := os.WriteFile(outPath, []byte(out), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "  error writing %s: %v\n", outPath, err)
			continue
		}
		fmt.Printf("  [+] Saved to: %s\n", outPath)
		processed++
	}

	fmt.Printf("All files processed. %d decrypted.\n", processed)
}
