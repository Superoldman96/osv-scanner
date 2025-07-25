// Package imodels defines internal models for osv-scanner.
package imodels

import (
	"fmt"
	"strings"

	"github.com/google/osv-scalibr/converter"
	"github.com/google/osv-scalibr/extractor"
	"github.com/google/osv-scalibr/extractor/filesystem/language/golang/gobinary"
	"github.com/google/osv-scalibr/extractor/filesystem/language/java/archive"
	archivemetadata "github.com/google/osv-scalibr/extractor/filesystem/language/java/archive/metadata"
	"github.com/google/osv-scalibr/extractor/filesystem/language/python/wheelegg"
	"github.com/google/osv-scalibr/extractor/filesystem/os/apk"
	apkmetadata "github.com/google/osv-scalibr/extractor/filesystem/os/apk/metadata"
	"github.com/google/osv-scalibr/extractor/filesystem/os/dpkg"
	dpkgmetadata "github.com/google/osv-scalibr/extractor/filesystem/os/dpkg/metadata"
	"github.com/google/osv-scalibr/extractor/filesystem/os/rpm"
	rpmmetadata "github.com/google/osv-scalibr/extractor/filesystem/os/rpm/metadata"
	"github.com/google/osv-scalibr/extractor/filesystem/sbom/cdx"
	"github.com/google/osv-scalibr/extractor/filesystem/sbom/spdx"
	"github.com/google/osv-scalibr/inventory"
	"github.com/google/osv-scanner/v2/internal/cachedregexp"
	"github.com/google/osv-scanner/v2/internal/cmdlogger"
	"github.com/google/osv-scanner/v2/internal/imodels/ecosystem"
	"github.com/google/osv-scanner/v2/internal/scalibrextract/language/javascript/nodemodules"
	"github.com/google/osv-scanner/v2/internal/scalibrextract/vcs/gitrepo"
	"github.com/google/osv-scanner/v2/internal/utility/purl"
	"github.com/google/osv-scanner/v2/internal/utility/semverlike"

	"github.com/google/osv-scanner/v2/pkg/models"
	"github.com/ossf/osv-schema/bindings/go/osvschema"

	scalibrosv "github.com/google/osv-scalibr/extractor/filesystem/osv"
)

var sbomExtractors = map[string]struct{}{
	spdx.Name: {},
	cdx.Name:  {},
}

var gitExtractors = map[string]struct{}{
	gitrepo.Name: {},
}

var osExtractors = map[string]struct{}{
	dpkg.Name: {},
	apk.Name:  {},
	rpm.Name:  {},
}

var artifactExtractors = map[string]struct{}{
	nodemodules.Name: {},
	gobinary.Name:    {},
	archive.Name:     {},
	wheelegg.Name:    {},
}

// PackageInfo provides getter functions for commonly used fields of inventory
// and applies transformations when required for use in osv-scanner
type PackageInfo struct {
	*extractor.Package

	// purlCache is used to cache the special case for SBOMs where we convert Name, Version, and Ecosystem from purls
	// extracted from the SBOM
	purlCache *models.PackageInfo
}

func (pkg *PackageInfo) Name() string {
	// TODO(v2): SBOM special case, to be removed after PURL to ESI conversion within each extractor is complete
	if pkg.purlCache != nil {
		return pkg.purlCache.Name
	}

	// --- Make specific patches to names as necessary ---
	// Patch Go package to stdlib
	if pkg.Ecosystem().Ecosystem == osvschema.EcosystemGo && pkg.Package.Name == "go" {
		return "stdlib"
	}

	// TODO: Move the normalization to another where matching logic happens.
	// Patch python package names to be normalized
	if pkg.Ecosystem().Ecosystem == osvschema.EcosystemPyPI {
		// per https://peps.python.org/pep-0503/#normalized-names
		return strings.ToLower(cachedregexp.MustCompile(`[-_.]+`).ReplaceAllLiteralString(pkg.Package.Name, "-"))
	}

	// Patch Maven archive extractor package names
	if metadata, ok := pkg.Metadata.(*archivemetadata.Metadata); ok {
		// Debian uses source name on osv.dev
		// (fallback to using the normal name if source name is empty)
		if metadata.ArtifactID != "" && metadata.GroupID != "" {
			return metadata.GroupID + ":" + metadata.ArtifactID
		}
	}

	// --- OS metadata ---
	if metadata, ok := pkg.Metadata.(*dpkgmetadata.Metadata); ok {
		// Debian uses source name on osv.dev
		// (fallback to using the normal name if source name is empty)
		if metadata.SourceName != "" {
			return metadata.SourceName
		}
	}

	if metadata, ok := pkg.Metadata.(*apkmetadata.Metadata); ok {
		if metadata.OriginName != "" {
			return metadata.OriginName
		}
	}

	return pkg.Package.Name
}

func (pkg *PackageInfo) Ecosystem() ecosystem.Parsed {
	ecosystemStr := pkg.Package.Ecosystem()

	// TODO(v2): SBOM special case, to be removed after PURL to ESI conversion within each extractor is complete
	if pkg.purlCache != nil {
		ecosystemStr = pkg.purlCache.Ecosystem
	}

	// TODO: Maybe cache this parse result
	eco, err := ecosystem.Parse(ecosystemStr)
	if err != nil {
		// Ignore this error for now as we can't do too much about an unknown ecosystem
		cmdlogger.Warnf("Warning: %s", err.Error())
	}

	return eco
}

func (pkg *PackageInfo) Version() string {
	// TODO(v2): SBOM special case, to be removed after PURL to ESI conversion within each extractor is complete
	if pkg.purlCache != nil {
		return pkg.purlCache.Version
	}

	// Assume Go stdlib patch version as the latest version
	//
	// This is done because go1.20 and earlier do not support patch
	// version in go.mod file, and will fail to build.
	//
	// However, if we assume patch version as .0, this will cause a lot of
	// false positives. This compromise still allows osv-scanner to pick up
	// when the user is using a minor version that is out-of-support.
	if pkg.Ecosystem().Ecosystem == osvschema.EcosystemGo && pkg.Name() == "stdlib" {
		v := semverlike.ParseSemverLikeVersion(pkg.Package.Version, 3)
		if len(v.Components) == 2 {
			return fmt.Sprintf(
				"%d.%d.%d",
				v.Components.Fetch(0),
				v.Components.Fetch(1),
				99,
			)
		}
	}

	return pkg.Package.Version
}

func (pkg *PackageInfo) Location() string {
	if len(pkg.Locations) > 0 {
		return pkg.Locations[0]
	}

	return ""
}

func (pkg *PackageInfo) Commit() string {
	if pkg.SourceCode != nil {
		return pkg.SourceCode.Commit
	}

	return ""
}

func (pkg *PackageInfo) SourceType() models.SourceType {
	if len(pkg.Plugins) == 0 {
		return models.SourceTypeUnknown
	}

	for _, extractorName := range pkg.Plugins {
		if _, ok := osExtractors[extractorName]; ok {
			return models.SourceTypeOSPackage
		} else if _, ok := sbomExtractors[extractorName]; ok {
			return models.SourceTypeSBOM
		} else if _, ok := gitExtractors[extractorName]; ok {
			return models.SourceTypeGit
		} else if _, ok := artifactExtractors[extractorName]; ok {
			return models.SourceTypeArtifact
		}
	}

	return models.SourceTypeProjectPackage
}

func (pkg *PackageInfo) DepGroups() []string {
	if dg, ok := pkg.Metadata.(scalibrosv.DepGroups); ok {
		return dg.DepGroups()
	}

	return []string{}
}

func (pkg *PackageInfo) OSPackageName() string {
	if metadata, ok := pkg.Metadata.(*apkmetadata.Metadata); ok {
		return metadata.PackageName
	}
	if metadata, ok := pkg.Metadata.(*dpkgmetadata.Metadata); ok {
		return metadata.PackageName
	}
	if metadata, ok := pkg.Metadata.(*rpmmetadata.Metadata); ok {
		return metadata.PackageName
	}

	return ""
}

// FromInventory converts an extractor.Package into a PackageInfo.
func FromInventory(inv *extractor.Package) PackageInfo {
	pi := PackageInfo{Package: inv}
	if pi.SourceType() == models.SourceTypeSBOM {
		purlStruct := converter.ToPURL(pi.Package)
		if purlStruct != nil {
			purlCache, _ := purl.ToPackage(purlStruct.String())
			pi.purlCache = &purlCache
		}
	}

	return pi
}

// PackageScanResult represents a package and its associated vulnerabilities and licenses.
// This struct is used to store the results of a scan at a per package level.
type PackageScanResult struct {
	PackageInfo PackageInfo
	// TODO: Use osvschema.Vulnerability instead
	Vulnerabilities []*osvschema.Vulnerability
	Licenses        []models.License
	LayerDetails    *extractor.LayerDetails

	// TODO(v2):
	// SourceAnalysis *SourceAnalysis
	// Any additional scan enrichment steps
}

// ScanResult represents the result of a scan, which will generally have packages
// but can also have more generic findings that are not related to packages
type ScanResult struct {
	PackageResults  []PackageScanResult
	GenericFindings []*inventory.GenericFinding
}
