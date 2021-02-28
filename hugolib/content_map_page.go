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
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gohugoio/hugo/common/types"
	"github.com/spf13/cast"

	"github.com/gohugoio/hugo/common/maps"

	"github.com/gohugoio/hugo/resources"

	"github.com/gohugoio/hugo/common/hugio"
	"github.com/gohugoio/hugo/hugofs"
	"github.com/gohugoio/hugo/hugofs/files"
	"github.com/gohugoio/hugo/parser/pageparser"
	"github.com/gohugoio/hugo/resources/page"
	"github.com/gohugoio/hugo/resources/resource"

	"github.com/gohugoio/hugo/common/para"
	"github.com/pkg/errors"
)

func newPageMaps(h *HugoSites) *pageMaps {
	mps := make([]*pageMap, len(h.Sites))
	for i, s := range h.Sites {
		mps[i] = s.pageMap
	}
	return &pageMaps{
		workers: para.New(h.numWorkers),
		pmaps:   mps,
	}
}

type pageMap struct {
	cfg contentMapConfig
	s   *Site

	*sectionMap
}

func (m *pageMap) Len() int {
	panic("TODO1 Len")
	/*
		l := 0
		for _, t := range m.contentMap.pageTrees {
			l += t.Pages.Len()
			l += t.Resources.Len()
		}
		return l
	*/
	return 0
}

func (m *pageMap) createMissingTaxonomyNodes() error {
	if m.cfg.taxonomyDisabled {
		return nil
	}
	//panic("TODO1")
	return nil
	/*
		m.taxonomyEntries.Pages.Walk(func(s string, v interface{}) bool {
			n := v.(*contentNode)
			vi := n.viewInfo
			k := cleanTreeKey(vi.name.plural + "/" + vi.termKey)

			if _, found := m.taxonomies.Pages.Get(k); !found {
				vic := &contentBundleViewInfo{
					name:       vi.name,
					termKey:    vi.termKey,
					termOrigin: vi.termOrigin,
				}
				m.taxonomies.Pages.Insert(k, &contentNode{viewInfo: vic})
			}
			return false
		})
	*/

	return nil
}

func (m *pageMap) newPageFromContentNode(
	s *Site,
	n *contentNode, parentBucket *pagesMapBucket, owner *pageState) (*pageState, error) {
	if n.fi == nil {
		panic("FileInfo must (currently) be set")
	}

	f, err := newFileInfo(s.SourceSpec, n.fi)
	if err != nil {
		return nil, err
	}

	meta := n.fi.Meta()
	content := func() (hugio.ReadSeekCloser, error) {
		return meta.Open()
	}

	bundled := owner != nil

	sections := s.sectionsFromFile(f)

	kind := s.kindFromFileInfoOrSections(f, sections)
	if kind == page.KindTerm {
		s.PathSpec.MakePathsSanitized(sections)
	}

	metaProvider := &pageMeta{kind: kind, sections: sections, bundled: bundled, s: s, f: f}

	ps, err := newPageBase(metaProvider)
	if err != nil {
		return nil, err
	}

	if n.fi.Meta().GetBool(walkIsRootFileMetaKey) {
		// Make sure that the bundle/section we start walking from is always
		// rendered.
		// This is only relevant in server fast render mode.
		ps.forceRender = true
	}

	n.p = ps
	if ps.IsNode() {
		ps.bucket = newPageBucket(parentBucket, ps)
	}

	gi, err := s.h.gitInfoForPage(ps)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load Git data")
	}
	ps.gitInfo = gi

	r, err := content()
	if err != nil {
		return nil, err
	}
	defer r.Close()

	parseResult, err := pageparser.Parse(
		r,
		pageparser.Config{EnableEmoji: s.siteCfg.enableEmoji},
	)
	if err != nil {
		return nil, err
	}

	ps.pageContent = pageContent{
		source: rawPageContent{
			parsed:         parseResult,
			posMainContent: -1,
			posSummaryEnd:  -1,
			posBodyStart:   -1,
		},
	}

	ps.shortcodeState = newShortcodeHandler(ps, ps.s, nil)

	if err := ps.mapContent(parentBucket, metaProvider); err != nil {
		return nil, ps.wrapError(err)
	}

	if err := metaProvider.applyDefaultValues(n); err != nil {
		return nil, err
	}

	ps.init.Add(func() (interface{}, error) {
		pp, err := newPagePaths(s, ps, metaProvider)
		if err != nil {
			return nil, err
		}

		outputFormatsForPage := ps.m.outputFormats()

		// Prepare output formats for all sites.
		// We do this even if this page does not get rendered on
		// its own. It may be referenced via .Site.GetPage and
		// it will then need an output format.
		ps.pageOutputs = make([]*pageOutput, len(ps.s.h.renderFormats))
		created := make(map[string]*pageOutput)
		shouldRenderPage := !ps.m.noRender()

		for i, f := range ps.s.h.renderFormats {
			if po, found := created[f.Name]; found {
				ps.pageOutputs[i] = po
				continue
			}

			render := shouldRenderPage
			if render {
				_, render = outputFormatsForPage.GetByName(f.Name)
			}

			po := newPageOutput(ps, pp, f, render)

			// Create a content provider for the first,
			// we may be able to reuse it.
			if i == 0 {
				contentProvider, err := newPageContentOutput(ps, po)
				if err != nil {
					return nil, err
				}
				po.initContentProvider(contentProvider)
			}

			ps.pageOutputs[i] = po
			created[f.Name] = po

		}

		if err := ps.initCommonProviders(pp); err != nil {
			return nil, err
		}

		return nil, nil
	})

	ps.parent = owner

	return ps, nil
}

func (m *contentBranchNode) newResource(fim hugofs.FileMetaInfo, owner *pageState) (resource.Resource, error) {
	if owner == nil {
		panic("owner is nil")
	}
	// TODO(bep) consolidate with multihost logic + clean up
	outputFormats := owner.m.outputFormats()
	seen := make(map[string]bool)
	var targetBasePaths []string
	// Make sure bundled resources are published to all of the output formats'
	// sub paths.
	for _, f := range outputFormats {
		p := f.Path
		if seen[p] {
			continue
		}
		seen[p] = true
		targetBasePaths = append(targetBasePaths, p)

	}

	meta := fim.Meta()
	r := func() (hugio.ReadSeekCloser, error) {
		return meta.Open()
	}

	target := strings.TrimPrefix(meta.Path(), owner.File().Dir())

	return owner.s.ResourceSpec.New(
		resources.ResourceSourceDescriptor{
			TargetPaths:        owner.getTargetPaths,
			OpenReadSeekCloser: r,
			FileInfo:           fim,
			RelTargetFilename:  target,
			TargetBasePaths:    targetBasePaths,
			LazyPublish:        !owner.m.buildConfig.PublishResources,
		})
}

func (m *pageMap) createSiteTaxonomies() error {
	m.s.taxonomies = make(TaxonomyList)
	for _, viewName := range m.cfg.taxonomyConfig {
		taxonomy := make(Taxonomy)
		m.s.taxonomies[viewName.plural] = taxonomy
		m.WalkBranchesPrefix(cleanTreeKey(viewName.plural)+"/", func(s string, b *contentBranchNode) bool {
			b.terms.Walk(func(s string, n *contentNode) bool {
				info := n.viewInfo
				taxonomy.add(info.termKey, page.NewWeightedPage(info.weight, info.ref.p, n.p))
				return false
			})
			return false
		})
	}

	for _, taxonomy := range m.s.taxonomies {
		for _, v := range taxonomy {
			v.Sort()
		}
	}

	//panic("TODO1")
	/*
		m.s.taxonomies = make(TaxonomyList)
		var walkErr error
		m.taxonomies.Pages.Walk(func(s string, v interface{}) bool {
			n := v.(*contentNode)
			t := n.viewInfo

			viewName := t.name

			if t.termKey == "" {
				m.s.taxonomies[viewName.plural] = make(Taxonomy)
			} else {
				taxonomy := m.s.taxonomies[viewName.plural]
				if taxonomy == nil {
					walkErr = errors.Errorf("missing taxonomy: %s", viewName.plural)
					return true
				}
				m.taxonomyEntries.Pages.WalkPrefix(s, func(ss string, v interface{}) bool {
					b2 := v.(*contentNode)
					info := b2.viewInfo
					taxonomy.add(info.termKey, page.NewWeightedPage(info.weight, info.ref.p, n.p))

					return false
				})
			}

			return false
		})

		for _, taxonomy := range m.s.taxonomies {
			for _, v := range taxonomy {
				v.Sort()
			}
		}

		return walkErr
	*/
	return nil
}

func (m *pageMap) createListAllPages() page.Pages {

	pages := make(page.Pages, 0)

	m.WalkPagesPrefixSection("", contentTreeNoListAlwaysFilter, func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool {
		if n.p == nil {
			panic(fmt.Sprintf("BUG: page not set for %q", s))
		}
		pages = append(pages, n.p)
		return false
	})

	page.SortByDefault(pages)
	return pages

	return nil
}

const (
	sectionHomeKey = ""
	sectionZeroKey = "ZERO"
)

func (m *pageMap) assembleSections() error {
	/*
		var sectionsToDelete []string
		var err error

		m.sections.Pages.Walk(func(s string, v interface{}) bool {
			n := v.(*contentNode)
			var shouldBuild bool

			defer func() {
				// Make sure we always rebuild the view cache.
				if shouldBuild && err == nil && n.p != nil {
					m.attachPageToViews(s, n)
					if n.p.IsHome() {
						m.s.home = n.p
					}
				}
			}()

			sections := m.splitKey(s)

			if n.p != nil {
				if n.p.IsHome() {
					m.s.home = n.p
				}
				shouldBuild = true
				return false
			}

			var parent *contentNode
			var parentBucket *pagesMapBucket

			const homeKey = ""

			if s != homeKey {
				_, parent = m.getSection(s)
				if parent == nil || parent.p == nil {
					panic(fmt.Sprintf("BUG: parent not set for %q", s))
				}
			}

			if parent != nil {
				parentBucket = parent.p.bucket
			}

			kind := page.KindSection
			if s == homeKey {
				kind = page.KindHome
			}

			if n.fi != nil {
				n.p, err = m.newPageFromContentNode(n, parentBucket, nil)
				if err != nil {
					return true
				}
			} else {
				n.p = m.s.newPage(n, parentBucket, kind, "", sections...)
			}

			shouldBuild = m.s.shouldBuild(n.p)
			if !shouldBuild {
				sectionsToDelete = append(sectionsToDelete, s)
				return false
			}

			n.p.treeRef = &contentTreeRef{
				m:   m,
				t:   m.sections,
				n:   n,
				key: s,
			}

			if err = m.assembleResources(s, n.p, parentBucket); err != nil {
				return true
			}

			return false
		})

		for _, s := range sectionsToDelete {
			m.deleteSectionByPath(s)
		}

		return err
	*/
	//	panic("TODO1")
	return nil
}

func (m *pageMap) assemblePages() error {
	// TODO1 distinguish between 1st and 2nd... builds (re check p nil etc.)
	// TODO1	m.taxonomyEntries.DeletePrefix("/")

	var err error

	// Holds references to sections or pages to exlude from the build
	// because front matter dictated it (e.g. a draft).
	var sectionsToDelete []string
	var pagesToDelete []*contentTreeRef
	defer func() {
		for _, p := range pagesToDelete {
			p.branch.pages.nodes.Delete(p.key)
			p.branch.resources.nodes.Delete(p.key + "/")
		}
		for _, s := range sectionsToDelete {
			m.sections.Delete(s)
			m.sections.DeletePrefix(s + "/")
		}
	}()

	handleBranch := func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool {
		if branch == nil && s != "" {
			panic(fmt.Sprintf("no branch set for branch %q", s))
		}

		if s != "" && branch.n.p == nil {
			panic(fmt.Sprintf("no page set on owner for for branch %q", s))
		}

		tref := &contentTreeRef{
			m:      m,
			branch: branch,
			key:    s,
		}

		if n.p != nil {
			if n.p.IsHome() {
				m.s.home = n.p
			}
			return false
		}

		var section *contentBranchNode
		section = branch // TODO1
		var bucket *pagesMapBucket
		kind := page.KindSection
		if s == "" {
			kind = page.KindHome
		}

		bucket = section.n.p.bucket
		// It may also be a taxonomy.
		// TODO1

		if n.fi != nil {
			n.p, err = m.newPageFromContentNode(m.s, n, bucket, nil)
			if err != nil {
				return true
			}
		} else {
			n.p = m.s.newPage(n, bucket, kind, "", m.splitKey(s)...)
		}

		n.p.treeRef = tref

		if n.p.IsHome() {
			m.s.home = n.p
		}

		if !m.s.shouldBuild(n.p) {
			if s == "" {
				// Home page
				// TODO1 a way to abort walking.
				return true

			}
			sectionsToDelete = append(sectionsToDelete, s)
		}

		return false
	}

	handlePage := func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool {

		tref := &contentTreeRef{
			m:      m,
			branch: branch,
			key:    s,
		}

		section := branch
		bucket := section.n.p.bucket
		kind := page.KindPage
		// It may also be a taxonomy term.
		if section.n.p.Kind() == page.KindTaxonomy {
			kind = page.KindTerm
		}

		if n.fi != nil {
			n.p, err = m.newPageFromContentNode(m.s, n, bucket, nil)
			if err != nil {
				return true
			}
		} else {
			n.p = m.s.newPage(n, bucket, kind, "", m.splitKey(s)...)
		}

		n.p.treeRef = tref

		if !m.s.shouldBuild(n.p) {
			pagesToDelete = append(pagesToDelete, tref)
		}

		parent := branch

		for {
			parent.n.p.m.Dates.UpdateDateAndLastmodIfAfter(n.p.m.Dates)
			if parent.n.p.bucket.parent == nil {
				break
			}
			parent = parent.n.p.bucket.parent.self.treeRef.branch // TODO1
			fmt.Println("PARE", parent.key, parent.n.p.m.Dates)
		}

		return false
	}

	handleResource := func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool {
		if owner.p == nil {
			panic("invalid state, page not set on resource owner")
		}

		p := owner.p

		// TODO1 error handling
		meta := n.fi.Meta()
		classifier := meta.Classifier()
		var r resource.Resource
		switch classifier {
		case files.ContentClassContent:
			var rp *pageState
			rp, err = m.newPageFromContentNode(m.s, n, branch.n.p.bucket, p)
			if err != nil {
				return true
			}
			rp.m.resourcePath = filepath.ToSlash(strings.TrimPrefix(rp.Path(), p.File().Dir()))
			r = rp

		case files.ContentClassFile:
			r, err = branch.newResource(n.fi, p)
			if err != nil {
				return true
			}
		default:
			panic(fmt.Sprintf("invalid classifier: %q", classifier))
		}

		p.resources = append(p.resources, r)

		return false
	}

	// Create home page if it does not exist.
	hn := m.Get("")
	if hn == nil {
		hn = m.InsertSection("", &contentNode{})
	}

	if hn.n.p == nil {

		if hn.n.fi != nil {
			hn.n.p, err = m.newPageFromContentNode(m.s, hn.n, nil, nil)
			if err != nil {
				return err
			}
		} else {
			hn.n.p = m.s.newPage(hn.n, nil, page.KindHome, "", "")
		}

		// TODO1 rework this treeRef thing
		hn.n.p.treeRef = &contentTreeRef{
			m:      m,
			branch: hn,
		}

	}

	m.s.home = hn.n.p

	// First create missing taxonomy nodes.
	for _, viewName := range m.cfg.taxonomyConfig {
		key := cleanTreeKey(viewName.plural)
		if m.Get(key) == nil {
			n := &contentNode{
				viewInfo: &contentBundleViewInfo{
					name: viewName,
				},
			}
			if m.s.home == nil {
				panic("home not set")
			}
			branch := m.InsertSection(key, n)
			n.p = m.s.newPage(n, m.s.home.bucket, page.KindTaxonomy, "", viewName.plural)
			n.p.treeRef = &contentTreeRef{
				m:      m,
				branch: branch,
				key:    key,
			}

		}
	}

	// First pass.
	m.Walk(
		sectionMapQuery{
			Exclude:      func(s string, n *contentNode) bool { return n.p != nil },
			SectionsFunc: nil,
			Branch: sectionMapQueryCallBacks{
				Key:      newSectionMapQueryKey(contentMapRoot, true),
				Page:     handleBranch,
				Resource: handleResource,
			},
			Leaf: sectionMapQueryCallBacks{
				Page:     handlePage,
				Resource: handleResource,
			},
		})

	if m.cfg.taxonomyDisabled {
		// Done.
		return nil
	}

	for _, viewName := range m.cfg.taxonomyConfig {
		taxonomy := m.Get(cleanTreeKey(viewName.plural))
		if taxonomy == nil {
			panic(fmt.Sprintf("tax not found")) // TODO1 move the creation here
		}

		handleTaxonomyEntries := func(b *contentBranchNode, owner *contentNode, s string, n *contentNode) bool {
			if n.p == nil {
				panic("page is nil")
			}
			vals := types.ToStringSlicePreserveString(getParam(n.p, viewName.plural, false))
			if vals == nil {
				return false
			}
			w := getParamToLower(n.p, viewName.plural+"_weight")
			weight, err := cast.ToIntE(w)
			if err != nil {
				m.s.Log.Errorf("Unable to convert taxonomy weight %#v to int for %q", w, n.p.Path())
				// weight will equal zero, so let the flow continue
			}

			for i, v := range vals {
				term := m.s.getTaxonomyKey(v)

				bv := &contentNode{
					p: n.p,
					viewInfo: &contentBundleViewInfo{
						ordinal:    i,
						name:       viewName,
						termKey:    term,
						termOrigin: v,
						weight:     weight,
						ref:        n,
					},
				}

				termKey := cleanTreeKey(term)
				taxonomyTermKey := taxonomy.key + termKey

				// It may have been added with the content files
				termBranch := m.Get(taxonomyTermKey)

				if termBranch == nil {

					vic := &contentBundleViewInfo{
						name:       viewName,
						termKey:    term,
						termOrigin: v,
					}

					n := &contentNode{viewInfo: vic}
					n.p = m.s.newPage(n, taxonomy.n.p.bucket, page.KindTerm, vic.term(), viewName.plural, term)

					termBranch = m.InsertSection(taxonomyTermKey, n)

					n.p.treeRef = &contentTreeRef{
						m:      m,
						branch: termBranch,
						key:    taxonomyTermKey,
					}
				}

				termBranch.terms.nodes.Insert(s, bv)

			}
			return false
		}

		m.Walk(
			sectionMapQuery{
				Branch: sectionMapQueryCallBacks{
					Key:  newSectionMapQueryKey(contentMapRoot, true),
					Page: handleTaxonomyEntries,
				},
				Leaf: sectionMapQueryCallBacks{
					Page: handleTaxonomyEntries,
				},
			})
	}

	// TODO 1	m.deleteOrphanSections()

	return nil
}

// TODO1 error handling
func (m *pageMap) attachPageToViews(s string, n *contentNode, b *contentBranchNode) error {
	if m.cfg.taxonomyDisabled {
		return nil
	}

	for _, viewName := range m.cfg.taxonomyConfig {
		vals := types.ToStringSlicePreserveString(getParam(n.p, viewName.plural, false))
		if vals == nil {
			continue
		}
		w := getParamToLower(n.p, viewName.plural+"_weight")
		weight, err := cast.ToIntE(w)
		if err != nil {
			m.s.Log.Errorf("Unable to convert taxonomy weight %#v to int for %q", w, n.p.Path())
			// weight will equal zero, so let the flow continue
		}

		for i, v := range vals {
			term := m.s.getTaxonomyKey(v)

			bv := &contentNode{
				p: n.p,
				viewInfo: &contentBundleViewInfo{
					ordinal:    i,
					name:       viewName,
					termKey:    term,
					termOrigin: v,
					weight:     weight,
					ref:        n,
				},
			}

			key := cleanTreeKey(path.Join(term, s))

			termKey := cleanTreeKey(term)
			b.terms.nodes.Insert(key, bv)
			// It may have been added from a content file.
			if !b.pages.Has(termKey) {
				vic := &contentBundleViewInfo{
					name:       viewName,
					termKey:    term,
					termOrigin: v,
				}
				n := &contentNode{viewInfo: vic}
				n.p = m.s.newPage(n, b.n.p.bucket, page.KindTerm, "", viewName.plural, term)

				n.p.treeRef = &contentTreeRef{
					m:      m,
					branch: b,
					key:    termKey,
				}

				b.pages.nodes.Insert(termKey, n)
			}
		}
	}

	return nil
}

/*
func (b *contentBranchNode) assembleResources(key string, p *pageState, parentBucket *pagesMapBucket) error {
	var err error

	tree := b.pageResources
	s := p.s

	tree.nodes.WalkPrefix(key, func(key2 string, v interface{}) bool {
		n := v.(*contentNode)
		meta := n.fi.Meta()
		classifier := meta.Classifier()
		var r resource.Resource
		switch classifier {
		case files.ContentClassContent:
			var rp *pageState
			rp, err = n.newPageFromContentNode(s, n, parentBucket, p)
			if err != nil {
				return true
			}
			rp.m.resourcePath = filepath.ToSlash(strings.TrimPrefix(rp.Path(), p.File().Dir()))
			r = rp

		case files.ContentClassFile:
			r, err = n.newResource(n.fi, p)
			if err != nil {
				return true
			}
		default:
			panic(fmt.Sprintf("invalid classifier: %q", classifier))
		}

		p.resources = append(p.resources, r)
		return false
	})

	return err
}
TODO1 remove
*/

// TODO1 consolidate
func (m *pageMap) assembleTaxonomies() error {
	return m.createSiteTaxonomies()
}

type pageMapQueryKey struct {
	Key string

	isSet    bool
	isPrefix bool
}

func (q pageMapQueryKey) IsPrefix() bool {
	return !q.IsZero() && q.isPrefix
}

func (q pageMapQueryKey) Eq(key string) bool {
	if q.IsZero() || q.isPrefix {
		return false
	}
	return q.Key == key
}

func (q pageMapQueryKey) IsZero() bool {
	return !q.isSet
}

func (q pageMapQuery) IsFiltered(s string, n *contentNode) bool {
	return q.Filter != nil && q.Filter(s, n)
}

type pageMapQuery struct {
	Leaf   pageMapQueryKey
	Branch pageMapQueryKey
	Filter contentTreeNodeFilter
}

func (m *pageMap) collectPages(query pageMapQuery, fn func(c *contentNode)) error {
	panic("TODO1 collectPages")
	/*
		if query.Filter == nil {
			query.Filter = contentTreeNoListAlwaysFilter
		}

		m.pages.WalkQuery(query, func(s string, n *contentNode) bool {
			fn(n)
			return false
		})
	*/

	return nil
}

func (m *pageMap) collectPagesAndSections(query pageMapQuery, fn func(c *contentNode)) error {
	panic("TODO1 collectPagesAndSections")
	if err := m.collectSections(query, fn); err != nil {
		return err
	}

	if err := m.collectPages(query, fn); err != nil {
		return err
	}

	return nil
}

func (m *pageMap) collectSections(query pageMapQuery, fn func(c *contentNode)) error {
	panic("TODO1 collectSections")
	/*
		m.sections.WalkQuery(query, func(s string, n *contentNode) bool {
			fn(n)
			return false
		})
	*/
	return nil
}

func (m *pageMap) collectSectionsRecursiveIncludingSelf(query pageMapQuery, fn func(c *contentNode)) error {
	// TODO1
	return nil

}

func (m *pageMap) collectTaxonomies(prefix string, fn func(c *contentNode)) error {
	panic("TODO1 collectTaxo")
	/*
		m.taxonomies.WalkQuery(pageMapQuery{Prefix: prefix}, func(s string, n *contentNode) bool {
			fn(n)
			return false
		})
	*/
	return nil
}

// withEveryBundlePage applies fn to every Page, including those bundled inside
// leaf bundles.
func (m *pageMap) withEveryBundlePage(fn func(p *pageState) bool) error {
	return m.withEveryBundleNode(func(n *contentNode) bool {
		if n.p != nil {
			return fn(n.p)
		}
		return false
	})
}

func (m *pageMap) withEveryBundleNode(fn func(n *contentNode) bool) error {
	callbackPage := func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool {
		return fn(n)
	}

	callbackResource := func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool {
		return fn(n)
	}

	q := sectionMapQuery{
		Exclude: func(s string, n *contentNode) bool { return n.p == nil },
		Branch: sectionMapQueryCallBacks{
			Key:      newSectionMapQueryKey("", true),
			Page:     callbackPage,
			Resource: callbackResource,
		},
		Leaf: sectionMapQueryCallBacks{
			Page:     callbackPage,
			Resource: callbackResource,
		},
	}

	return m.Walk(q)
}

type pageMaps struct {
	workers *para.Workers
	pmaps   []*pageMap
}

// deleteSection deletes the entire section from s.
func (m *pageMaps) deleteSection(s string) {
	m.withMaps(func(pm *pageMap) error {
		pm.sectionMap.sections.Delete(s)
		return nil
	})
}

func (m *pageMaps) AssemblePages() error {

	return m.withMaps(func(pm *pageMap) error {
		/*if err := pm.CreateMissingNodes(); err != nil {
			return err
		}*/

		if err := pm.assemblePages(); err != nil {
			return err
		}

		/*
			if err := pm.createMissingTaxonomyNodes(); err != nil {
				return err
			}
		*/
		// Handle any new sections created in the step above.
		// TODO1 remove?
		/*if err := pm.assembleSections(); err != nil {
			return err
		}*/

		if pm.s.home == nil {
			panic("TODO1 home is nil")
			// Home is disabled, everything is.
			pm.sections.DeletePrefix("")
			return nil
		}

		/*if err := pm.assembleTaxonomies(); err != nil {
			return err
		}

		if err := pm.createSiteTaxonomies(); err != nil {
			return err
		}*/

		/* TODO1

		sw := &sectionWalker{m: pm.sectionMaps}
		a := sw.applyAggregates()
		_, mainSectionsSet := pm.s.s.Info.Params()["mainsections"]
		if !mainSectionsSet && a.mainSection != "" {
			mainSections := []string{strings.TrimRight(a.mainSection, "/")}
			pm.s.s.Info.Params()["mainSections"] = mainSections
			pm.s.s.Info.Params()["mainsections"] = mainSections
		}

		pm.s.lastmod = a.datesAll.Lastmod()
		if resource.IsZeroDates(pm.s.home) {
			pm.s.home.m.Dates = a.datesAll
		}
		*/

		return nil
	})

}

func (m *pageMaps) walkBundles(fn func(n *contentNode) bool) error {
	return m.withMaps(func(pm *pageMap) error {
		return pm.withEveryBundleNode(fn)
	})
}

func (m *pageMaps) walkBranchesPrefix(prefix string, fn func(s string, n *contentNode) bool) error {
	return m.withMaps(func(pm *pageMap) error {
		callbackPage := func(branch *contentBranchNode, owner *contentNode, s string, n *contentNode) bool {
			return fn(s, n)
		}

		q := sectionMapQuery{
			OnlyBranches: true,
			Branch: sectionMapQueryCallBacks{
				Key:  newSectionMapQueryKey(prefix, true),
				Page: callbackPage,
			},
		}

		return pm.Walk(q)
	})
}

func (m *pageMaps) withMaps(fn func(pm *pageMap) error) error {
	g, _ := m.workers.Start(context.Background())
	for _, pm := range m.pmaps {
		pm := pm
		g.Run(func() error {
			return fn(pm)
		})
	}
	return g.Wait()
}

type pagesMapBucket struct {
	// Cascading front matter.
	cascade map[page.PageMatcher]maps.Params

	parent *pagesMapBucket // The parent bucket, nil if the home page. TODO1 optimize LongestPrefix to use this.
	self   *pageState      // The branch node (TODO1) move p.n here?

	*pagesMapBucketPages
}

type pagesMapBucketPages struct {
	pagesInit sync.Once
	pages     page.Pages

	pagesAndSectionsInit sync.Once
	pagesAndSections     page.Pages

	sectionsInit sync.Once
	sections     page.Pages
}

func (b *pagesMapBucket) getRegularPagesRecursive() page.Pages {
	pages := b.self.treeRef.getRegularPagesRecursive()
	page.SortByDefault(pages)
	return pages
}

func (b *pagesMapBucket) getPagesAndSections() page.Pages {
	b.pagesAndSectionsInit.Do(func() {

		if b.self.treeRef == nil {
			panic("TODO1 nil get for " + b.self.Kind())
		}

		b.pagesAndSections = b.self.treeRef.getPagesAndSections()
	})
	return b.pagesAndSections
}

func (b *pagesMapBucket) getSections() page.Pages {
	b.sectionsInit.Do(func() {
		if b.self.treeRef == nil {
			return
		}
		b.sections = b.self.treeRef.getSections()
	})

	return b.sections
}

func (b *pagesMapBucket) getTaxonomies() page.Pages {
	// TODO1 b.sections/init
	ref := b.self.treeRef
	if ref == nil {
		return nil
	}
	var pas page.Pages
	ref.branch.pages.WalkPrefix(ref.key+"/", func(s string, n *contentNode) bool {
		pas = append(pas, n.p)
		return false
	})
	page.SortByDefault(pas)
	return pas
}

func (b *pagesMapBucket) getTaxonomyEntries() page.Pages {
	ref := b.self.treeRef
	if ref == nil {
		return nil
	}
	var pas page.Pages
	ref.branch.terms.WalkPrefix(ref.key+"/", func(s string, n *contentNode) bool {
		pas = append(pas, n.p)
		return false
	})
	page.SortByDefault(pas)
	return pas
}

type sectionAggregate struct {
	datesAll             resource.Dates
	datesSection         resource.Dates
	pageCount            int
	mainSection          string
	mainSectionPageCount int
}

type sectionAggregateHandler struct {
	sectionAggregate
	sectionPageCount int

	// Section
	b *contentNode
	s string
}

func (h *sectionAggregateHandler) String() string {
	return fmt.Sprintf("%s/%s - %d - %s", h.sectionAggregate.datesAll, h.sectionAggregate.datesSection, h.sectionPageCount, h.s)
}

func (h *sectionAggregateHandler) isRootSection() bool {
	return h.s != "/" && strings.Count(h.s, "/") == 2
}

func (h *sectionAggregateHandler) handleNested(v sectionWalkHandler) error {
	nested := v.(*sectionAggregateHandler)
	h.sectionPageCount += nested.pageCount
	h.pageCount += h.sectionPageCount
	h.datesAll.UpdateDateAndLastmodIfAfter(nested.datesAll)
	h.datesSection.UpdateDateAndLastmodIfAfter(nested.datesAll)
	return nil
}

func (h *sectionAggregateHandler) handlePage(s string, n *contentNode) error {
	h.sectionPageCount++

	var d resource.Dated
	if n.p != nil {
		d = n.p
	} else if n.viewInfo != nil && n.viewInfo.ref != nil {
		d = n.viewInfo.ref.p
	} else {
		return nil
	}

	h.datesAll.UpdateDateAndLastmodIfAfter(d)
	h.datesSection.UpdateDateAndLastmodIfAfter(d)
	return nil
}

func (h *sectionAggregateHandler) handleSectionPost() error {
	if h.sectionPageCount > h.mainSectionPageCount && h.isRootSection() {
		h.mainSectionPageCount = h.sectionPageCount
		h.mainSection = strings.TrimPrefix(h.s, "/")
	}

	if resource.IsZeroDates(h.b.p) {
		h.b.p.m.Dates = h.datesSection
	}

	h.datesSection = resource.Dates{}

	return nil
}

func (h *sectionAggregateHandler) handleSectionPre(s string, b *contentNode) error {
	h.s = s
	h.b = b
	h.sectionPageCount = 0
	h.datesAll.UpdateDateAndLastmodIfAfter(b.p)
	return nil
}

type sectionWalkHandler interface {
	handleNested(v sectionWalkHandler) error
	handlePage(s string, b *contentNode) error
	handleSectionPost() error
	handleSectionPre(s string, b *contentNode) error
}

type sectionWalker struct {
	err error
}

func (w *sectionWalker) applyAggregates() *sectionAggregateHandler {
	return w.walkLevel("/", func() sectionWalkHandler {
		return &sectionAggregateHandler{}
	}).(*sectionAggregateHandler)
}

func (w *sectionWalker) walkLevel(prefix string, createVisitor func() sectionWalkHandler) sectionWalkHandler {
	// TODO1
	/*
		level := strings.Count(prefix, "/")
		prefix = helpers.AddTrailingSlash(prefix)

		visitor := createVisitor()

		for _, stree := range w.m.pageTrees {
			w.m.taxonomies.Sections.WalkPrefix(prefix, func(s string, v interface{}) bool {
				currentLevel := strings.Count(s, "/")

				if currentLevel > level+1 {
					return false
				}

				n := v.(*contentNode)
				tree := v.(*sectionTreePages)

				if w.err = visitor.handleSectionPre(s, n); w.err != nil {
					return true
				}

				tree.pages.Walk(func(ss string, v interface{}) bool {
					n := v.(*contentNode)
					w.err = visitor.handlePage(ss, n)
					return w.err != nil
				})

				w.err = visitor.handleSectionPost()

				return w.err != nil
			})
		}

		w.m.sections.Pages.WalkPrefix(prefix, func(s string, v interface{}) bool {
			currentLevel := strings.Count(s, "/")
			if currentLevel > level+1 {
				return false
			}

			n := v.(*contentNode)

			if w.err = visitor.handleSectionPre(s, n); w.err != nil {
				return true
			}

			w.m.pages.Pages.WalkPrefix(s+contentMapNodeSeparator, func(s string, v interface{}) bool {
				w.err = visitor.handlePage(s, v.(*contentNode))
				return w.err != nil
			})

			if w.err != nil {
				return true
			}

			nested := w.walkLevel(s, createVisitor)
			if w.err = visitor.handleNested(nested); w.err != nil {
				return true
			}

			w.err = visitor.handleSectionPost()

			return w.err != nil
		})

		return visitor
	*/
	return nil
}

type viewName struct {
	singular string // e.g. "category"
	plural   string // e.g. "categories"
}

func (v viewName) IsZero() bool {
	return v.singular == ""
}
