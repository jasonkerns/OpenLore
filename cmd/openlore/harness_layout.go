package main

import (
	"os"
	"path/filepath"
)

// harnessLayout is the folder layout a new knowledge base starts with: one
// server for all projects, organised around OpenSpec (https://openspec.dev).
// Agent Harness will give each kind of folder its own menu, by path. Changes are
// worked on in each project's repository; the specs here are copies published
// from it.
var harnessLayout = map[string]string{
	"README.md": `# Knowledge base

Shared by everyone's agents. Start here, then look in the folder that fits.

| Folder | What goes there |
|---|---|
| ` + "`projects/<project>/`" + ` | One folder per project (repository): ` + "`project.md`" + ` (repo, stack, conventions), its OpenSpec specs, changes in review and archived changes |
| ` + "`standards/`" + ` | Specs that apply to every project (auth, logging, API style), as ` + "`standards/<capability>/spec.md`" + ` |
| ` + "`ideas/`" + ` | Ideas and questions not yet worked on; explore one or turn it into a proposal |
| ` + "`explorations/`" + ` | Write-ups from exploring an idea or problem (` + "`/opsx:explore`" + `) |
| ` + "`decisions/`" + ` | Decision records: context, options, decision, consequences |
| ` + "`runbooks/`" + ` | How to do things: deploys, recoveries, set-ups |
| ` + "`skills/`" + ` | Agent Skills for the whole team: one folder per skill, each with a ` + "`SKILL.md`" + ` |

Specs follow OpenSpec's format. A project's specs are worked on in its repository (` + "`openspec/`" + `) and copied
here when a change is archived, so don't edit ` + "`projects/*/specs/`" + ` or ` + "`projects/*/changes/`" + ` here: propose a change instead.
`,
	"projects/README.md": `# Projects

One folder per project, named like its repository:

` + "```" + `
<project>/
├── project.md                      # repo URL, stack, conventions (the context in openspec/config.yaml)
├── specs/<capability>/spec.md      # current specs, copied from the repo's openspec/specs
├── changes/<change>/               # changes in review or being applied, copied from openspec/changes
└── archive/YYYY-MM-DD-<change>/    # finished changes
` + "```" + `
`,
	"standards/README.md": `# Standards

Specs that every project follows, one folder per capability: ` + "`<capability>/spec.md`" + `, in OpenSpec's format
(` + "`## Requirements`" + `, then ` + "`### Requirement: …`" + ` with ` + "`#### Scenario: …`" + ` under each).
`,
	"ideas/README.md": `# Ideas

One document per idea or open question: what and why, in a few lines. When someone picks one up it becomes an
exploration or a change proposal; link to it from here.
`,
	"explorations/README.md": `# Explorations

Write-ups from exploring an idea or problem before committing to a change: what was looked at, options, findings
and a recommendation. Name them ` + "`YYYY-MM-DD-<topic>.md`" + `.
`,
	"decisions/README.md": `# Decisions

One record per decision, ` + "`NNNN-<title>.md`" + `: context, the options considered, the decision, and its
consequences. Superseded records stay, with a link to the one that replaces them.
`,
	"runbooks/README.md": `# Runbooks

Step-by-step procedures: what it's for, prerequisites, the steps with exact commands, how to check it worked and
how to undo it.
`,
	// Each immediate child of a skills collection must be a skill, so this
	// folder gets no README (the root README describes it).
	"skills/": "",
}

// harnessAllowedFiles is the file types the server takes: upstream's defaults
// (documents, data, images) plus scripts, which Agent Skills often include.
var harnessAllowedFiles = []string{
	`"*.md"`, `"*.markdown"`, `"*.txt"`, `"*.html"`, `"*.htm"`, `"*.css"`, `"*.js"`,
	`"*.json"`, `"*.jsonl"`, `"*.yaml"`, `"*.yml"`, `"*.csv"`, `"*.tsv"`, `"*.xml"`, `"*.toml"`,
	`"*.png"`, `"*.jpg"`, `"*.jpeg"`, `"*.gif"`, `"*.svg"`, `"*.webp"`,
	`"*.sh"`, `"*.bash"`, `"*.ps1"`, `"*.py"`, `"*.mjs"`, `"*.cjs"`, `"*.ts"`, `"*.rb"`,
}

// seedHarnessLayout writes harnessLayout into docs when docs is empty, and
// reports whether it did. A docs folder with anything in it is left alone.
func seedHarnessLayout(docs string) (bool, error) {
	entries, err := os.ReadDir(docs)
	if err != nil || len(entries) > 0 {
		return false, err
	}
	for rel, body := range harnessLayout {
		path := filepath.Join(docs, filepath.FromSlash(rel))
		if body == "" {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return false, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return false, err
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return false, err
		}
	}
	return true, nil
}
