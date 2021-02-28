// Copyright 2019 The Hugo Authors. All rights reserved.
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
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkg/errors"

	"github.com/gohugoio/hugo/helpers"

	"github.com/gohugoio/hugo/resources/page"

	"github.com/gohugoio/hugo/hugofs/files"

	"github.com/gohugoio/hugo/hugofs"

	radix "github.com/armon/go-radix"
)

// TODO1 remove one
const (
	contentMapNodeSeparator = "/"
	cmLeafSeparator         = "/"
	// The home page.
	contentMapRoot = ""
)

// Used to mark ambiguous keys in reverse index lookups.
var ambiguousContentNode = &contentNode{}

func newContentMap(cfg contentMapConfig) *contentMap {
	m := &contentMap{
		cfg:             &cfg,
		pages:           newContentTree("pages"),
		sections:        newContentTree("sections"),
		taxonomies:      newContentTree("taxonomies"),
		taxonomyEntries: newContentTree("taxonomyEntries"),
	}

	m.pageTrees = []*contentTree{
		m.pages, m.sections, m.taxonomies,
	}

	m.branchTrees = []*contentTree{
		m.sections, m.taxonomies,
	}

	addToReverseMap := func(k string, n *contentNode, m map[interface{}]*contentNode) {
		k = strings.ToLower(k)
		existing, found := m[k]
		if found && existing != ambiguousContentNode {
			m[k] = ambiguousContentNode
		} else if !found {
			m[k] = n
		}
	}

	m.pageReverseIndex = &contentTreeReverseIndex{
		t: []*contentTree{m.pages, m.sections, m.taxonomies},
		contentTreeReverseIndexMap: &contentTreeReverseIndexMap{
			initFn: func(t *contentTree, m map[interface{}]*contentNode) {
				t.Pages.Walk(func(s string, v interface{}) bool {
					n := v.(*contentNode)
					if n.p != nil && !n.p.File().IsZero() {
						meta := n.p.File().FileInfo().Meta()
						if meta.Path() != meta.PathFile() {
							// Keep track of the original mount source.
							mountKey := filepath.ToSlash(filepath.Join(meta.Module(), meta.PathFile()))
							addToReverseMap(mountKey, n, m)
						}
					}
					k := strings.TrimPrefix(strings.TrimSuffix(path.Base(s), cmLeafSeparator), contentMapNodeSeparator)
					addToReverseMap(k, n, m)
					return false
				})
			},
		},
	}

	return m
}

type contentBundleViewInfo struct {
	ordinal    int
	name       viewName
	termKey    string
	termOrigin string
	weight     int
	ref        *contentNode
}

func (c *contentBundleViewInfo) kind() string {
	if c.termKey != "" {
		return page.KindTerm
	}
	return page.KindTaxonomy
}

func (c *contentBundleViewInfo) sections() []string {
	if c.kind() == page.KindTaxonomy {
		return []string{c.name.plural}
	}

	return []string{c.name.plural, c.termKey}
}

func (c *contentBundleViewInfo) term() string {
	if c.termOrigin != "" {
		return c.termOrigin
	}

	return c.termKey
}

// We store the branch nodes in either the `sections` or `taxonomies` tree
// with their path as a key; Unix style slashes with leading, but no leading slash.
//
// E.g. "blog/" or "categories/funny/"
//
// The home page is stored in `sections` with an empty key, so the sections can
// be fetched with:
//
//    sections.Get("") // => home page
//    sections.Get("/blog") // => blog section
//
// Regular pages are stored in the `pages` tree and Resources in the respective
// `resoures` tree.
//
// All share the same key structure, enabling a common pattern for navigating the data:
//
//    pages.Get("/blog/my-post")
//    resources.WalkPrefix("/blog/my-post/, ...)
//	  sections.WalkPrefix("/", ...) // /blog
//    sections.LongestPrefix("/blog/my-post") // /blog
//
type contentMap struct {
	cfg *contentMapConfig

	// View of regular pages, sections, and taxonomies.
	pageTrees contentTrees

	// View of sections and taxonomies.
	branchTrees contentTrees

	// Stores page bundles keyed by its path's directory or the base filename,
	// e.g. "blog/post.md" => "/blog/post", "blog/post/index.md" => "/blog/post"
	// These are the "regular pages" and all of them are bundles.
	pages *contentTree

	// A reverse index used as a fallback in GetPage.
	// There are currently two cases where this is used:
	// 1. Short name lookups in ref/relRef, e.g. using only "mypage.md" without a path.
	// 2. Links resolved from a remounted content directory. These are restricted to the same module.
	// Both of the above cases can  result in ambigous lookup errors.
	pageReverseIndex *contentTreeReverseIndex

	// Section nodes.
	sections *contentTree

	// Taxonomy nodes.
	taxonomies *contentTree

	// Pages in a taxonomy.
	taxonomyEntries *contentTree
}

func (m *contentMap) AddFiles(fis ...hugofs.FileMetaInfo) error {
	for _, fi := range fis {
		if err := m.addFile(fi); err != nil {
			return err
		}
	}

	return nil
}

func (m *contentMap) AddFilesBundle(header hugofs.FileMetaInfo, resources ...hugofs.FileMetaInfo) error {
	var (
		meta       = header.Meta()
		classifier = meta.Classifier()
		isBranch   = classifier == files.ContentClassBranch
		bundlePath = m.getBundleDir(meta)

		n = m.newContentNodeFromFi(header)

		section string
		bundle  string

		tree *contentTree
	)

	panic("TODO1")

	if isBranch {
		// Either a section or a taxonomy node.
		section = bundlePath
		if tc := m.cfg.getTaxonomyConfig(section); !tc.IsZero() {
			term := strings.TrimPrefix(strings.TrimPrefix(section, "/"+tc.plural), "/")

			n.viewInfo = &contentBundleViewInfo{
				name:       tc,
				termKey:    term,
				termOrigin: term,
			}

			n.viewInfo.ref = n
			bundle = cleanTreeKey(section)
			tree = m.taxonomies
			m.taxonomies.Pages.Insert(bundle, n)
		} else {
			bundle = section
			tree = m.sections
			key := cleanTreeKey(section)
			m.sections.Pages.Insert(key, n)
		}
	} else {
		panic("TODO1")
		// A regular page. Attach it to its section.
		// TODO1
		section, _ = m.getOrCreateSection(n, bundlePath)
		n.section = section
		bundle = cleanTreeKey(bundlePath)
		m.pages.Pages.Insert(bundle, n)
		tree = m.pages
	}

	if m.cfg.isRebuild {
		// The resource owner will be either deleted or overwritten on rebuilds,
		// but make sure we handle deletion of resources (images etc.) as well.
		// TODO1 b.ForResource("").DeleteAll()
	}

	for _, r := range resources {
		key := cleanTreeKey(r.Meta().Path())
		tree.Resources.Insert(key, &contentNode{fi: r})
	}

	return nil
}

func (m *pageMap) CreateMissingNodes() error {
	if m.s.Lang() == "no" {
		//m.debug(m.s.Lang(), os.Stdout)
	}
	// TODO1 remove?
	// Create missing home and root sections
	/*rootSections := make(map[string]interface{})
	trackRootSection := func(s string, b *contentNode) {
		parts := strings.Split(s, "/")
		if len(parts) > 2 {
			root := strings.TrimSuffix(parts[1], contentMapNodeSeparator)
			if root != "" {
				if _, found := rootSections[root]; !found {
					rootSections[root] = b
				}
			}
		}
	}

	m.Walk(sectionMapQuery{
		Branch: sectionMapQueryCallBacks{
			NoRecurse: false,
			Key:       newSectionMapQueryKey(contentMapRoot, true),
			Branch: func(s string, n *contentNode) bool {
				trackRootSection(s, n)
			},
		},
	})

	if _, found := rootSections[contentMapRoot]; !found {
		rootSections[contentMapRoot] = true
	}

	for sect, v := range rootSections {
		var sectionPath string
		if n, ok := v.(*contentNode); ok && n.path != "" {
			sectionPath = n.path
			firstSlash := strings.Index(sectionPath, "/")
			if firstSlash != -1 {
				sectionPath = sectionPath[:firstSlash]
			}
		}
		sect = cleanTreeKey(sect)
		_, found := m.main.Sections.Get(sect)
		if !found {
			m.main.Sections.Insert(sect, newContentBranchNode(&contentNode{path: sectionPath}))
		}
	}

	for _, view := range m.cfg.taxonomyConfig {
		s := cleanTreeKey(view.plural)
		_, found := m.taxonomies.Sections.Get(s)
		if !found {
			b := &contentNode{
				viewInfo: &contentBundleViewInfo{
					name: view,
				},
			}
			b.viewInfo.ref = b
			// TODO1 make a typed Insert
			m.taxonomies.Sections.Insert(s, newContentBranchNode(b))
		}
	}*/

	return nil
}

func (m *contentMap) getBundleDir(meta hugofs.FileMeta) string {
	dir := cleanTreeKey(filepath.Dir(meta.Path()))

	switch meta.Classifier() {
	case files.ContentClassContent:
		return path.Join(dir, meta.TranslationBaseName())
	default:
		return dir
	}
}

func (m *pageMap) getBundleDir(meta hugofs.FileMeta) string {
	dir := cleanTreeKey(filepath.Dir(meta.Path()))

	switch meta.Classifier() {
	case files.ContentClassContent:
		return path.Join(dir, meta.TranslationBaseName())
	default:
		return dir
	}
}

func (m *contentMap) newContentNodeFromFi(fi hugofs.FileMetaInfo) *contentNode {
	return &contentNode{
		fi:   fi,
		path: strings.TrimPrefix(filepath.ToSlash(fi.Meta().Path()), "/"),
	}
}

func (m *pageMap) newContentNodeFromFi(fi hugofs.FileMetaInfo) *contentNode {
	return &contentNode{
		fi: fi,
		// TODO1 used for?
		path: strings.TrimPrefix(filepath.ToSlash(fi.Meta().Path()), "/"),
	}
}

func (m *sectionMap) getFirstSection(s string) (string, *contentNode) {
	for {
		k, v, found := m.sections.LongestPrefix(s)

		if !found {
			return "", nil
		}

		// /blog
		if strings.Count(k, "/") <= 1 {
			return k, v.(*contentBranchNode).n
		}

		s = path.Dir(s)

	}

}

func (m *contentMap) getOrCreateSection(n *contentNode, s string) (string, *contentNode) {
	level := strings.Count(s, "/")
	k, b := m.getSection(s)

	mustCreate := false

	if k == "" {
		mustCreate = true
	} else if level > 1 && k == "" {
		// We found the home section, but this page needs to be placed in
		// the root, e.g. "/blog", section.
		mustCreate = true
	}

	if !mustCreate {
		return k, b
	}

	k = cleanTreeKey(s[:strings.Index(s[1:], "/")+1])

	b = &contentNode{
		path: n.rootSection(),
	}

	m.sections.Pages.Insert(k, b)

	return k, b
}

func (m *contentMap) getPage(section, name string) *contentNode {
	section = helpers.AddTrailingSlash(section)
	key := helpers.AddTrailingSlash(section + name)

	v, found := m.pages.Pages.Get(key)
	if found {
		return v.(*contentNode)
	}
	return nil
}

func (m *contentMap) getSection(s string) (string, *contentNode) {
	s = helpers.AddTrailingSlash(path.Dir(strings.TrimSuffix(s, "/")))

	k, v, found := m.sections.Pages.LongestPrefix(s)

	if found {
		return k, v.(*contentNode)
	}
	return "", nil
}

func (m *contentMap) getTaxonomyParent(s string) (string, *contentNode) {
	s = helpers.AddTrailingSlash(path.Dir(strings.TrimSuffix(s, "/")))
	k, v, found := m.taxonomies.Pages.LongestPrefix(s)

	if found {
		return k, v.(*contentNode)
	}

	v, found = m.sections.Pages.Get("")
	if found {
		return s, v.(*contentNode)
	}

	return "", nil
}

func (m *contentMap) addFile(fi hugofs.FileMetaInfo) error {
	key := cleanTreeKey(fi.Meta().Path())
	_, tree := m.pageTrees.LongestPrefix(key)
	if tree == nil {
		return errors.Errorf("tree for %q not found", key)
	}
	tree.Resources.Insert(key, &contentNode{fi: fi})
	return nil
}

// The home page is represented with the zero string.
// All other keys starts with a leading slash. No leading slash.
// Slashes are Unix-style.
func cleanTreeKey(k string) string {
	k = strings.ToLower(strings.Trim(path.Clean(filepath.ToSlash(k)), "./"))
	if k == "" || k == "/" {
		return ""
	}
	return helpers.AddLeadingSlash(k)
}

func (m *contentMap) onSameLevel(s1, s2 string) bool {
	return strings.Count(s1, "/") == strings.Count(s2, "/")
}

func (m *contentMap) deletePageByPath(s string) {
	m.pages.Pages.Walk(func(s string, v interface{}) bool {
		return false
	})
}

func (m *contentMap) reduceKeyPart(dir, filename string) string {
	dir, filename = filepath.ToSlash(dir), filepath.ToSlash(filename)
	dir, filename = strings.TrimPrefix(dir, "/"), strings.TrimPrefix(filename, "/")

	return strings.TrimPrefix(strings.TrimPrefix(filename, dir), "/")
}

func (m *contentMap) splitKey(k string) []string {
	if k == "" || k == "/" {
		return nil
	}

	return strings.Split(k, "/")[1:]
}

type contentMapConfig struct {
	lang                 string
	taxonomyConfig       []viewName
	taxonomyDisabled     bool
	taxonomyTermDisabled bool
	pageDisabled         bool
	isRebuild            bool
}

func (cfg contentMapConfig) getTaxonomyConfig(s string) (v viewName) {
	s = strings.TrimPrefix(s, "/")
	if s == "" {
		return
	}
	for _, n := range cfg.taxonomyConfig {
		if strings.HasPrefix(s, n.plural) {
			return n
		}
	}

	return
}

type contentNode struct {
	p *pageState

	// Set for taxonomy nodes.
	viewInfo *contentBundleViewInfo

	// Set if source is a file.
	// We will soon get other sources.
	fi hugofs.FileMetaInfo

	// The owning bundle key.
	// TODO1 remove
	section string

	// The source path. Unix slashes. No leading slash.
	path string
}

// Returns whether this is a taxonomy node (a taxonomy or a term).
func (b *contentNode) isTaxonomyNode() bool {
	return b.viewInfo != nil
}

func (b *contentNode) rootSection() string {
	if b.path == "" {
		return ""
	}
	firstSlash := strings.Index(b.path, "/")
	if firstSlash == -1 {
		return ""
	}
	return b.path[:firstSlash]
}

// TODO1 names
type ctree struct {
	*radix.Tree
}

func (m *pageMap) AddFilesBundle(header hugofs.FileMetaInfo, resources ...hugofs.FileMetaInfo) error {

	var (
		meta       = header.Meta()
		classifier = meta.Classifier()
		isBranch   = classifier == files.ContentClassBranch
		key        = cleanTreeKey(m.getBundleDir(meta))
		n          = m.newContentNodeFromFi(header)

		pageTree *contentBranchNode
	)

	if !isBranch && m.cfg.pageDisabled {
		return nil
	}

	if isBranch {
		// Either a section or a taxonomy node.
		if tc := m.cfg.getTaxonomyConfig(key); !tc.IsZero() {
			term := strings.TrimPrefix(strings.TrimPrefix(key, "/"+tc.plural), "/")
			n.viewInfo = &contentBundleViewInfo{
				name:       tc,
				termKey:    term,
				termOrigin: term,
			}

			n.viewInfo.ref = n
			pageTree = m.InsertSection(key, n)

		} else {
			key := cleanTreeKey(key)
			pageTree = m.InsertSection(key, n)
		}
	} else {
		// A regular page. Attach it to its section.
		_, pageTree = m.getOrCreateSection(n, key)
		if pageTree == nil {
			panic(fmt.Sprintf("NO section %s", key))
		}
		pageTree.InsertPage(key, n)
	}

	if m.cfg.isRebuild {
		// The resource owner will be either deleted or overwritten on rebuilds,
		// but make sure we handle deletion of resources (images etc.) as well.
		// TODO1 b.ForResource("").DeleteAll()
	}

	for _, r := range resources {
		key := cleanTreeKey(r.Meta().Path())
		pageTree.pageResources.nodes.Insert(key, &contentNode{fi: r})
	}

	return nil
}

func (m *pageMap) getOrCreateSection(n *contentNode, s string) (string, *contentBranchNode) {
	level := strings.Count(s, "/")

	k, pageTree := m.LongestPrefix(s)

	mustCreate := false

	if pageTree == nil {
		mustCreate = true
	} else if level > 1 && k == "" {
		// We found the home section, but this page needs to be placed in
		// the root, e.g. "/blog", section.
		mustCreate = true
	} else {
		return k, pageTree
	}

	if !mustCreate {
		return k, pageTree
	}

	k = cleanTreeKey(s[:strings.Index(s[1:], "/")+1])

	n = &contentNode{
		path: n.rootSection(), // TODO1
	}

	if k != "" {
		// Make sure we always have the root/home node.
		if m.Get("") == nil {
			m.InsertSection("", &contentNode{})
		}
	}

	pageTree = m.InsertSection(k, n)
	return k, pageTree
}

type contentTree struct {
	Name      string
	Pages     *radix.Tree
	Resources *radix.Tree
}

func newContentTree(name string) *contentTree {
	return &contentTree{
		Name:      name,
		Pages:     radix.New(),
		Resources: radix.New(),
	}
}

type contentTrees []*contentTree

func (t contentTrees) LongestPrefix(s string) (string, *contentTree) {
	var matchKey string
	var matchTree *contentTree

	for _, tree := range t {
		p, _, found := tree.Pages.LongestPrefix(s)
		if found && len(matchKey) > len(matchKey) {
			matchKey = p
			matchTree = tree
		}
	}

	return matchKey, matchTree
}

type contentTreeNodeFilter func(s string, n *contentNode) bool
type contentTreeNodeCallback func(s string, n *contentNode) bool
type contentTreeBranchNodeCallback func(s string, current *contentBranchNode) bool

type contentTreeOwnerNodeCallback func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool

type walkContentTreeCallbacksO struct {
	// Either of these may be nil, but not all.
	page     contentTreeNodeCallback
	leaf     contentTreeNodeCallback
	branch   contentTreeBranchNodeCallback
	resource contentTreeNodeCallback
}

func newcontentTreeNodeCallbackChain(callbacks ...contentTreeNodeCallback) contentTreeNodeCallback {
	return func(s string, n *contentNode) bool {
		for i, cb := range callbacks {
			// Allow the last callback to stop the walking.
			if i == len(callbacks)-1 {
				return cb(s, n)
			}

			if cb(s, n) {
				// Skip the rest of the callbacks, but continue walking.
				return false
			}
		}
		return false
	}
}

func newWalkContentTreeCallbacksPage(cb contentTreeNodeCallback) walkContentTreeCallbacksO {
	return walkContentTreeCallbacksO{
		page: cb,
	}
}

var (
	contentTreeNoListAlwaysFilter = func(s string, n *contentNode) bool {
		if n.p == nil {
			return true
		}
		return n.p.m.noListAlways()
	}

	contentTreeNoRenderFilter = func(s string, n *contentNode) bool {
		if n.p == nil {
			return true
		}
		return n.p.m.noRender()
	}

	contentTreeNoLinkFilter = func(s string, n *contentNode) bool {
		if n.p == nil {
			return true
		}
		return n.p.m.noLink()
	}

	contentTreeNoopFilter = func(s string, n *contentNode) bool {
		return false
	}
)

func (c *ctree) WalkQuery(query pageMapQuery, walkFn contentTreeNodeCallback) {
	/*filter := query.Filter
	if filter == nil {
		// TODO1 check callers
		// TODO1 check if filter stop is needed.
		//filter = contentTreeNoListAlwaysFilter
		filter = contentTreeNoopFilter
	}

	if query.Leaf != "" {
		c.WalkPrefix(query.Leaf, func(s string, v interface{}) bool {
			n := v.(*contentNode)
			skip, stop := filter(s, n)
			if skip {
				return stop
			}
			return walkFn(s, n) || stop
		})

		return
	}

	c.Walk(func(s string, v interface{}) bool {
		n := v.(*contentNode)
		skip, stop := filter(s, n)
		if skip {
			return stop
		}
		return walkFn(s, n) || stop
	})
	*/
}

func (c contentTrees) WalkRenderable(fn contentTreeNodeCallback) {
	panic("TODO1 WalkRenarable")
	/*
		query := pageMapQuery{Filter: contentTreeNoRenderFilter}
		for _, tree := range c {
			tree.WalkQuery(query, fn)
		}
	*/
}

func (c contentTrees) WalkLinkable(fn contentTreeNodeCallback) {
	panic("TODO1 WalkLinkable")
	/*
		query := pageMapQuery{Filter: contentTreeNoLinkFilter}
		for _, tree := range c {
			tree.WalkQuery(query, fn)
		}
	*/
}

func (c contentTrees) WalkPages(fn contentTreeNodeCallback) {
	for _, tree := range c {
		tree.Pages.Walk(func(s string, v interface{}) bool {
			n := v.(*contentNode)
			return fn(s, n)
		})
	}
}

func (c contentTrees) WalkPagesAndResources(fn contentTreeNodeCallback) {
	c.WalkPages(fn)
	for _, tree := range c {
		tree.Resources.Walk(func(s string, v interface{}) bool {
			n := v.(*contentNode)
			return fn(s, n)
		})
	}
}

func (c contentTrees) WalkPrefix(prefix string, fn contentTreeNodeCallback) {
	for _, tree := range c {
		tree.Pages.WalkPrefix(prefix, func(s string, v interface{}) bool {
			n := v.(*contentNode)
			return fn(s, n)
		})
	}
}

func (c *contentTree) getMatch(matches func(b *contentNode) bool) string {
	var match string
	c.Pages.Walk(func(s string, v interface{}) bool {
		n, ok := v.(*contentNode)
		if !ok {
			return false
		}

		if matches(n) {
			match = s
			return true
		}

		return false
	})

	return match
}

func (c *contentTree) hasBelow(s1 string) bool {
	var t bool
	s1 = helpers.AddTrailingSlash(s1)
	c.Pages.WalkPrefix(s1, func(s2 string, v interface{}) bool {
		t = true
		return true
	})
	return t
}

func (c *contentTree) printKeys() {
	c.Pages.Walk(func(s string, v interface{}) bool {
		fmt.Print(s)
		if n, ok := v.(*contentNode); ok {
			fmt.Print(" - ", n.section)
		}
		fmt.Println()
		return false
	})
}

func (c *contentTree) printKeysPrefix(prefix string) {
	c.Pages.WalkPrefix(prefix, func(s string, v interface{}) bool {
		return false
	})
}

// contentTreeRef points to a branch node in the given map.
type contentTreeRef struct {
	m      *pageMap // TODO1 used?
	branch *contentBranchNode
	key    string
}

func (c *contentTreeRef) getCurrentSection() (string, *contentNode) {
	if c.isSection() {
		return c.key, c.branch.n
	}
	return c.getSection()
}

func (c *contentTreeRef) isSection() bool {
	return c.branch.n.p.IsSection()
}

func (c *contentTreeRef) getSection() (string, *contentNode) {
	return c.key, c.branch.n
}

func (c *contentTreeRef) getRegularPagesRecursive() page.Pages {
	var pas page.Pages

	q := sectionMapQuery{
		Exclude: c.branch.n.p.m.getListFilter(true),
		Branch: sectionMapQueryCallBacks{
			Key: newSectionMapQueryKey(c.key+"/", true),
		},
		Leaf: sectionMapQueryCallBacks{
			Page: func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool {
				pas = append(pas, n.p)
				return false
			},
		},
	}

	c.m.Walk(q)

	page.SortByDefault(pas)

	return pas
}

func (c *contentTreeRef) getPagesAndSections() page.Pages {
	var pas page.Pages

	c.m.WalkPagesPrefixSectionNoRecurse(
		c.key+"/",
		c.branch.n.p.m.getListFilter(true),
		func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool {
			pas = append(pas, n.p)
			return false
		},
	)

	page.SortByDefault(pas)

	return pas
}

func (c *contentTreeRef) getSections() page.Pages {
	var pas page.Pages

	q := sectionMapQuery{
		NoRecurse: true,
		Exclude:   c.branch.n.p.m.getListFilter(true),
		Branch: sectionMapQueryCallBacks{
			Key: newSectionMapQueryKey(c.key+"/", true),
			Page: func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool {
				pas = append(pas, n.p)
				return false
			},
		},
	}

	c.m.Walk(q)

	page.SortByDefault(pas)

	return pas
}

type contentTreeReverseIndex struct {
	t []*contentTree
	*contentTreeReverseIndexMap
}

type contentTreeReverseIndexMap struct {
	m      map[interface{}]*contentNode
	init   sync.Once
	initFn func(*contentTree, map[interface{}]*contentNode)
}

func (c *contentTreeReverseIndex) Reset() {
	c.contentTreeReverseIndexMap = &contentTreeReverseIndexMap{
		initFn: c.initFn,
	}
}

func (c *contentTreeReverseIndex) Get(key interface{}) *contentNode {
	c.init.Do(func() {
		c.m = make(map[interface{}]*contentNode)
		for _, tree := range c.t {
			c.initFn(tree, c.m)
		}
	})
	return c.m[key]
}
