// Copyright 2026 The go-yaml Project Contributors
// SPDX-License-Identifier: Apache-2.0

// Tests that dumping a Node tree does not modify the tree.
//
// Dumping used to remove inferable tags from the caller's nodes, and add
// quoting styles to them, while working out what to emit
// (https://github.com/yaml/go-yaml/issues/371).

package yaml_test

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"github.com/pb33f/go-yaml"
	"github.com/pb33f/go-yaml/internal/testutil/assert"
)

// readOnlyDumpYAML covers the tags and styles that dumping elides or adds.
const readOnlyDumpYAML = `# head comment
plain: string
int: 42
float: 1.5
bool: true
nothing: ~
hex: 0x1F
inf: .inf
timestamp: 2001-12-14t21:59:43.10-05:00
date: 2002-12-14
yaml11-bool: yes
quoted:
  int: '123'
  bool: "true"
  float: '3.14'
  null: "null"
  timestamp: '2001-12-14'
  merge: '<<'
  version: "1.0" # line comment
blocks:
  literal: |
    line one
    line two
  folded: >
    folded
    text
base: &base
  type: object
  required: [id, name]
derived:
  <<: *base
  extra: value
multi-merge:
  <<: [*base, {more: 1}]
aliases:
- &scalar anchored scalar
- *scalar
- *base
tagged:
  str: !!str 123
  int: !!int "42"
  float: !!float 1
  bool: !!bool "false"
  null: !!null ""
  timestamp: !!timestamp 2001-12-14
  binary: !!binary R0lGODlhAQABAIAAAP///wAAACwAAAAAAQABAAACAkQBADs=
  custom: !custom value
  set: !!set {a, b}
  omap: !!omap [a: 1, b: 2]
  map: !!map {k: v}
  seq: !!seq [1, 2]
flow: {list: [1, "2", three], map: {k: v}}
empty-map: {}
empty-seq: []
# foot comment
`

// parseReadOnlyDumpDoc parses readOnlyDumpYAML and adds nodes built in code.
// Built nodes usually carry a tag but no style, so dumping must quote the
// strings among them that would otherwise resolve to another type.
func parseReadOnlyDumpDoc(t *testing.T) *yaml.Node {
	t.Helper()

	var doc yaml.Node
	assert.NoError(t, yaml.Unmarshal([]byte(readOnlyDumpYAML), &doc))

	scalar := func(tag, value string) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
	}
	var setString yaml.Node
	setString.SetString("true")
	seq := &yaml.Node{
		Kind:    yaml.SequenceNode,
		Tag:     "!!seq",
		Content: []*yaml.Node{scalar("!!int", "7"), scalar("!!str", "7")},
	}
	built := &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			scalar("!!str", "int-string"), scalar("!!str", "123"),
			scalar("!!str", "bool-string"), scalar("!!str", "false"),
			scalar("!!str", "null-string"), scalar("!!str", "~"),
			scalar("!!str", "timestamp-string"), scalar("!!str", "2001-12-14"),
			scalar("!!str", "merge-string"), scalar("!!str", "<<"),
			scalar("!!str", "long-tag"), scalar("tag:yaml.org,2002:str", "456"),
			scalar("!!str", "set-string"), &setString,
			scalar("!!str", "float-int"), scalar("!!float", "1"),
			scalar("!!merge", "<<"), scalar("!!str", "merged"),
			scalar("!!str", "seq"), seq,
		},
	}
	root := doc.Content[0]
	root.Content = append(root.Content, scalar("!!str", "built"), built)
	return &doc
}

// nodeState holds the fields of a node that dumping must leave alone.
type nodeState struct {
	node        *yaml.Node
	kind        yaml.Kind
	style       yaml.Style
	tag         string
	value       string
	anchor      string
	alias       *yaml.Node
	content     int
	headComment string
	lineComment string
	footComment string
	line        int
	column      int
}

func (s nodeState) String() string {
	return fmt.Sprintf("kind=%d style=%d tag=%q value=%q anchor=%q "+
		"alias=%t content=%d comments=%q/%q/%q",
		s.kind, s.style, s.tag, s.value, s.anchor, s.alias != nil, s.content,
		s.headComment, s.lineComment, s.footComment)
}

// snapshotNodes records the state of every node in the tree rooted at n, in
// depth-first order.
// Aliases are recorded but not followed, as the nodes they refer to are part
// of the tree already.
func snapshotNodes(n *yaml.Node) []nodeState {
	var states []nodeState
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		states = append(states, nodeState{
			node:        n,
			kind:        n.Kind,
			style:       n.Style,
			tag:         n.Tag,
			value:       n.Value,
			anchor:      n.Anchor,
			alias:       n.Alias,
			content:     len(n.Content),
			headComment: n.HeadComment,
			lineComment: n.LineComment,
			footComment: n.FootComment,
			line:        n.Line,
			column:      n.Column,
		})
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(n)
	return states
}

// diffNodes describes how the tree rooted at root differs from want.
func diffNodes(want []nodeState, root *yaml.Node) []string {
	got := snapshotNodes(root)
	if len(got) != len(want) {
		msg := fmt.Sprintf("tree has %d nodes; want %d", len(got), len(want))
		return []string{msg}
	}
	var diffs []string
	for i := range want {
		var diff string
		switch {
		case got[i].node != want[i].node:
			diff = fmt.Sprintf("was replaced: %v", want[i])
		case got[i] != want[i]:
			diff = fmt.Sprintf("changed\n got: %v\nwant: %v", got[i], want[i])
		default:
			continue
		}
		diffs = append(diffs, fmt.Sprintf("node at %d:%d %s",
			want[i].line, want[i].column, diff))
	}
	return diffs
}

func assertNodesUnchanged(t *testing.T, want []nodeState, root *yaml.Node) {
	t.Helper()
	for _, diff := range diffNodes(want, root) {
		t.Error(diff)
	}
}

// nodeMarshaler returns a node it does not own from MarshalYAML.
type nodeMarshaler struct {
	node *yaml.Node
}

func (m nodeMarshaler) MarshalYAML() (any, error) {
	return m.node, nil
}

// nodeDumpers dump a document node through each API that accepts nodes,
// either directly or inside other values.
var nodeDumpers = []struct {
	name string
	dump func(doc *yaml.Node) ([]byte, error)
}{{
	name: "Marshal",
	dump: func(doc *yaml.Node) ([]byte, error) {
		return yaml.Marshal(doc)
	},
}, {
	name: "Marshal root",
	dump: func(doc *yaml.Node) ([]byte, error) {
		return yaml.Marshal(doc.Content[0])
	},
}, {
	name: "Marshal value",
	dump: func(doc *yaml.Node) ([]byte, error) {
		return yaml.Marshal(*doc.Content[0])
	},
}, {
	name: "Marshal struct",
	dump: func(doc *yaml.Node) ([]byte, error) {
		return yaml.Marshal(&struct {
			Pointer *yaml.Node
			Value   yaml.Node
		}{doc.Content[0], *doc.Content[0]})
	},
}, {
	name: "Marshal map",
	dump: func(doc *yaml.Node) ([]byte, error) {
		return yaml.Marshal(map[string]*yaml.Node{"root": doc.Content[0]})
	},
}, {
	name: "Marshal slice",
	dump: func(doc *yaml.Node) ([]byte, error) {
		return yaml.Marshal([]*yaml.Node{doc.Content[0], doc.Content[0]})
	},
}, {
	name: "Marshal marshaler",
	dump: func(doc *yaml.Node) ([]byte, error) {
		return yaml.Marshal(nodeMarshaler{doc.Content[0]})
	},
}, {
	name: "Encoder",
	dump: func(doc *yaml.Node) ([]byte, error) {
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(4)
		if err := enc.Encode(doc); err != nil {
			return nil, err
		}
		err := enc.Close()
		return buf.Bytes(), err
	},
}, {
	name: "Dump",
	dump: func(doc *yaml.Node) ([]byte, error) {
		return yaml.Dump(doc)
	},
}, {
	name: "Dump double quotes",
	dump: func(doc *yaml.Node) ([]byte, error) {
		return yaml.Dump(doc, yaml.WithQuotePreference(yaml.QuoteDouble))
	},
}, {
	name: "Dump flow simple collections",
	dump: func(doc *yaml.Node) ([]byte, error) {
		return yaml.Dump(doc, yaml.WithFlowSimpleCollections())
	},
}, {
	name: "Dump all documents",
	dump: func(doc *yaml.Node) ([]byte, error) {
		return yaml.Dump([]*yaml.Node{doc, doc}, yaml.WithAllDocuments())
	},
}, {
	name: "Dumper",
	dump: func(doc *yaml.Node) ([]byte, error) {
		var buf bytes.Buffer
		d, err := yaml.NewDumper(&buf, yaml.WithV2Defaults())
		if err != nil {
			return nil, err
		}
		if err := d.Dump(doc); err != nil {
			return nil, err
		}
		err = d.Close()
		return buf.Bytes(), err
	},
}, {
	name: "Node.Encode",
	dump: func(doc *yaml.Node) ([]byte, error) {
		var n yaml.Node
		if err := n.Encode(doc); err != nil {
			return nil, err
		}
		return yaml.Marshal(&n)
	},
}, {
	name: "Node.Dump",
	dump: func(doc *yaml.Node) ([]byte, error) {
		var n yaml.Node
		opt := yaml.WithQuotePreference(yaml.QuoteSingle)
		if err := n.Dump(doc, opt); err != nil {
			return nil, err
		}
		return yaml.Marshal(&n)
	},
}}

func TestDumpNodeLeavesTreeUnchanged(t *testing.T) {
	for _, d := range nodeDumpers {
		t.Run(d.name, func(t *testing.T) {
			doc := parseReadOnlyDumpDoc(t)
			want := snapshotNodes(doc)

			first, err := d.dump(doc)
			assert.NoError(t, err)
			assertNodesUnchanged(t, want, doc)

			second, err := d.dump(doc)
			assert.NoError(t, err)
			assert.Equal(t, string(first), string(second))
		})
	}
}

func TestDumpNodeOutputUnaffectedByEarlierDumps(t *testing.T) {
	// Each dumper runs on a tree that every dumper before it has dumped
	// already, possibly with other options.
	shared := parseReadOnlyDumpDoc(t)
	for _, d := range nodeDumpers {
		want, err := d.dump(parseReadOnlyDumpDoc(t))
		assert.NoError(t, err)
		got, err := d.dump(shared)
		assert.NoError(t, err)
		assert.Equalf(t, string(want), string(got),
			"%s output changed by earlier dumps", d.name)
	}
}

func TestDumpNodeConcurrently(t *testing.T) {
	// Run with -race: dumping must only read the tree, so it can be dumped
	// and read from several goroutines at once.
	doc := parseReadOnlyDumpDoc(t)
	want := snapshotNodes(doc)
	outputs := make([][]byte, len(nodeDumpers))
	for i, d := range nodeDumpers {
		out, err := d.dump(parseReadOnlyDumpDoc(t))
		assert.NoError(t, err)
		outputs[i] = out
	}

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i, d := range nodeDumpers {
				out, err := d.dump(doc)
				if err != nil {
					t.Errorf("%s: %v", d.name, err)
					return
				}
				if !bytes.Equal(outputs[i], out) {
					t.Errorf("%s: concurrent dump output differs:\n"+
						" got: %s\nwant: %s", d.name, out, outputs[i])
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if diffs := diffNodes(want, doc); len(diffs) > 0 {
					t.Errorf("tree changed while being dumped: %s", diffs[0])
					return
				}
			}
		}()
	}
	wg.Wait()
	assertNodesUnchanged(t, want, doc)
}
