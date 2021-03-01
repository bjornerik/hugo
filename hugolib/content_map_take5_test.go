// Copyright 2020 The Hugo Authors. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hugolib

import (
	"fmt"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestSectionMap(t *testing.T) {
	c := qt.New(t)

	m := newSectionMap()

	c.Run("Tree relation", func(c *qt.C) {
		for _, test := range []struct {
			name   string
			s1     string
			s2     string
			expect int
		}{
			{"Sibling", "/blog/sub1", "/blog/sub2", 1},
			{"Root child", "", "/blog", 0},
			{"Child", "/blog/sub1", "/blog/sub1/sub2", 0},
			{"New root", "/blog/sub1", "/docs/sub2", -1},
		} {

			c.Run(test.name, func(c *qt.C) {
				c.Assert(m.treeRelation(test.s1, test.s2), qt.Equals, test.expect)
			})
		}

	})

	home, blog, blog_sub, blog_sub2, docs, docs_sub := &contentNode{path: "/"}, &contentNode{path: "/blog"}, &contentNode{path: "/blog/sub"}, &contentNode{path: "/blog/sub2"}, &contentNode{path: "/docs"}, &contentNode{path: "/docs/sub"}
	docs_sub2, docs_sub2_sub := &contentNode{path: "/docs/sub2"}, &contentNode{path: "/docs/sub2/sub"}

	article1, article2 := &contentNode{}, &contentNode{}

	image1, image2, image3 := &contentNode{}, &contentNode{}, &contentNode{}
	json1, json2, json3 := &contentNode{}, &contentNode{}, &contentNode{}
	xml1, xml2 := &contentNode{}, &contentNode{}

	c.Assert(m.InsertSection("", home), qt.Not(qt.IsNil))
	c.Assert(m.InsertSection("/docs", docs), qt.Not(qt.IsNil))
	c.Assert(m.InsertResource("/docs/data1.json", json1), qt.IsNil)
	c.Assert(m.InsertSection("/docs/sub", docs_sub), qt.Not(qt.IsNil))
	c.Assert(m.InsertResource("/docs/sub/data2.json", json2), qt.IsNil)
	c.Assert(m.InsertSection("/docs/sub2", docs_sub2), qt.Not(qt.IsNil))
	c.Assert(m.InsertResource("/docs/sub2/data1.xml", xml1), qt.IsNil)
	c.Assert(m.InsertSection("/docs/sub2/sub", docs_sub2_sub), qt.Not(qt.IsNil))
	c.Assert(m.InsertResource("/docs/sub2/sub/data2.xml", xml2), qt.IsNil)
	c.Assert(m.InsertSection("/blog", blog), qt.Not(qt.IsNil))
	c.Assert(m.InsertResource("/blog/logo.png", image3), qt.IsNil)
	c.Assert(m.InsertSection("/blog/sub", blog_sub), qt.Not(qt.IsNil))
	c.Assert(m.InsertSection("/blog/sub2", blog_sub2), qt.Not(qt.IsNil))
	c.Assert(m.InsertResource("/blog/sub2/data3.json", json3), qt.IsNil)

	// TODO1 filter tests
	blogSection := m.Get("/blog")
	c.Assert(blogSection.n, qt.Equals, blog)

	_, section := m.LongestPrefix("/blog/asdfadf")
	c.Assert(section, qt.Equals, blogSection)

	blogSection.InsertPage("/blog/my-article", article1)
	blogSection.InsertPage("/blog/my-article2", article2)
	c.Assert(blogSection.InsertResource("/blog/my-article/sunset.jpg", image1), qt.IsNil)
	c.Assert(blogSection.InsertResource("/blog/my-article2/sunrise.jpg", image2), qt.IsNil)

	c.Run("Sections start from root", func(c *qt.C) {
		var n int
		expect := [][]*contentNode{
			[]*contentNode{home},
			[]*contentNode{home, blog},
			[]*contentNode{home, blog, blog_sub},
			[]*contentNode{home, blog, blog_sub2},
			[]*contentNode{home, docs},
			[]*contentNode{home, docs, docs_sub},
			[]*contentNode{home, docs, docs_sub2},
			[]*contentNode{home, docs, docs_sub2, docs_sub2_sub},
		}
		q := sectionMapQuery{
			Branch: sectionMapQueryCallBacks{
				Key: newSectionMapQueryKey("", true),
			},
			SectionsFunc: func(sections []*contentBranchNode) {
				c.Assert(len(expect) > n, qt.Equals, true)
				for i, nm := range sections {
					c.Assert(len(expect[n]) > i, qt.Equals, true, qt.Commentf("n-%d-%d", n, i))
					c.Assert(nm.n.path, qt.Equals, expect[n][i].path, qt.Commentf("n-%d-%d", n, i))
				}
				n++
			},
		}

		c.Assert(m.Walk(q), qt.IsNil)
	})

	c.Run("Sections start one level down", func(c *qt.C) {
		var n int
		expect := [][]*contentNode{
			[]*contentNode{blog},
			[]*contentNode{blog, blog_sub},
			[]*contentNode{blog, blog_sub2},
		}
		q := sectionMapQuery{
			Branch: sectionMapQueryCallBacks{
				Key: newSectionMapQueryKey("/blog", true),
			},
			SectionsFunc: func(sections []*contentBranchNode) {
				c.Assert(len(expect) > n, qt.Equals, true)
				for i, nm := range sections {
					c.Assert(len(expect[n]) > i, qt.Equals, true, qt.Commentf("n-%d-%d", n, i))
					c.Assert(nm.n.path, qt.Equals, expect[n][i].path, qt.Commentf("n-%d-%d", n, i))
				}
				n++
			},
		}

		c.Assert(m.Walk(q), qt.IsNil)
	})

	type querySpec struct {
		key              string
		isBranchKey      bool
		isPrefix         bool
		noRecurse        bool
		doBranch         bool
		doBranchResource bool
		doPage           bool
		doPageResource   bool
	}

	type queryResult struct {
		query  sectionMapQuery
		result []string
	}

	newQuery := func(spec querySpec) *queryResult {
		qr := &queryResult{}

		addResult := func(typ, key string) {
			qr.result = append(qr.result, fmt.Sprintf("%s:%s", typ, key))
		}

		var (
			handleSection        func(branch, owner *contentBranchNode, s string, n *contentNode) bool
			handlePage           func(branch, owner *contentBranchNode, s string, n *contentNode) bool
			handleLeafResource   func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool
			handleBranchResource func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool

			keyBranch sectionMapQueryKey
			keyLeaf   sectionMapQueryKey
		)

		if spec.isBranchKey {
			keyBranch = newSectionMapQueryKey(spec.key, spec.isPrefix)
		} else {
			keyLeaf = newSectionMapQueryKey(spec.key, spec.isPrefix)
		}

		if spec.doBranch {
			handleSection = func(branch, owner *contentBranchNode, s string, n *contentNode) bool {
				addResult("section", s)
				return false
			}
		}

		if spec.doPage {
			handlePage = func(branch, owner *contentBranchNode, s string, n *contentNode) bool {
				addResult("page", s)
				return false
			}
		}

		if spec.doPageResource {
			handleLeafResource = func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool {
				addResult("resource", s)
				return false
			}
		}

		if spec.doBranchResource {
			handleBranchResource = func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool {
				addResult("resource-branch", s)
				return false
			}
		}

		qr.query = sectionMapQuery{
			NoRecurse: spec.noRecurse,
			Branch: sectionMapQueryCallBacks{
				Key:      keyBranch,
				Page:     handleSection,
				Resource: handleBranchResource,
			},
			Leaf: sectionMapQueryCallBacks{
				Key:      keyLeaf,
				Page:     handlePage,
				Resource: handleLeafResource,
			},
		}

		return qr

	}

	for _, test := range []struct {
		name   string
		spec   querySpec
		expect []string
	}{
		{
			"Branch",
			querySpec{key: "/blog", isBranchKey: true, doBranch: true},
			[]string{"section:/blog"},
		},
		{
			"Branch pages",
			querySpec{key: "/blog", isBranchKey: true, doPage: true},
			[]string{"page:/blog/my-article", "page:/blog/my-article2"},
		},
		{
			"Branch resources",
			querySpec{key: "/docs/", isPrefix: true, isBranchKey: true, doBranchResource: true},
			[]string{"resource-branch:/docs/sub/data2.json", "resource-branch:/docs/sub2/data1.xml", "resource-branch:/docs/sub2/sub/data2.xml"},
		},
		{
			"Branch section and resources",
			querySpec{key: "/docs/", isPrefix: true, isBranchKey: true, doBranch: true, doBranchResource: true},
			[]string{"section:/docs/sub", "resource-branch:/docs/sub/data2.json", "section:/docs/sub2", "resource-branch:/docs/sub2/data1.xml", "section:/docs/sub2/sub", "resource-branch:/docs/sub2/sub/data2.xml"},
		},
		{
			"Branch section and page resources",
			querySpec{key: "/blog", isPrefix: false, isBranchKey: true, doBranchResource: true, doPageResource: true},
			[]string{"resource-branch:/blog/logo.png", "resource:/blog/my-article/sunset.jpg", "resource:/blog/my-article2/sunrise.jpg"},
		},
		{
			"Branch section and pages",
			querySpec{key: "/blog", isBranchKey: true, doBranch: true, doPage: true},
			[]string{"section:/blog", "page:/blog/my-article", "page:/blog/my-article2"},
		},
		{
			"Branch pages and resources",
			querySpec{key: "/blog", isBranchKey: true, doPage: true, doPageResource: true},
			[]string{"page:/blog/my-article", "resource:/blog/my-article/sunset.jpg", "page:/blog/my-article2", "resource:/blog/my-article2/sunrise.jpg"},
		},
		{
			"Leaf page",
			querySpec{key: "/blog/my-article", isBranchKey: false, doPage: true},
			[]string{"page:/blog/my-article"},
		},
		{
			"Leaf page and resources",
			querySpec{key: "/blog/my-article", isBranchKey: false, doPage: true, doPageResource: true},
			[]string{"page:/blog/my-article", "resource:/blog/my-article/sunset.jpg"},
		},
		{
			"Root sections",
			querySpec{key: "/", isBranchKey: true, isPrefix: true, doBranch: true, noRecurse: true},
			[]string{"section:/blog", "section:/docs"},
		},
		{
			"All sections",
			querySpec{key: "", isBranchKey: true, isPrefix: true, doBranch: true},
			[]string{"section:", "section:/blog", "section:/blog/sub", "section:/blog/sub2", "section:/docs", "section:/docs/sub", "section:/docs/sub2", "section:/docs/sub2/sub"},
		},
	} {
		c.Run(test.name, func(c *qt.C) {
			qr := newQuery(test.spec)
			c.Assert(m.Walk(qr.query), qt.IsNil)
			c.Assert(qr.result, qt.DeepEquals, test.expect)
		})
	}
}
