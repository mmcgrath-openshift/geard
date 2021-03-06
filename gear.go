package geard

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Identifier string

var InvalidIdentifier = Identifier("")
var allowedIdentifier = regexp.MustCompile("\\A[a-fA-F0-9]{4,32}\\z")

func NewIdentifier(s string) (Identifier, error) {
	switch {
	case s == "":
		return InvalidIdentifier, errors.New("Gear identifier may not be empty")
	case !allowedIdentifier.MatchString(s):
		return InvalidIdentifier, errors.New("Gear identifier must match " + allowedIdentifier.String())
	}
	return Identifier(s), nil
}

func (g Identifier) UnitPathFor() string {
	return filepath.Join(basePath, "units", g.UnitNameFor())
}

func (g Identifier) UnitNameFor() string {
	return fmt.Sprintf("gear-%s.service", g)
}

func (g Identifier) UnitNameForJob() string {
	return fmt.Sprintf("job-%s.service", g)
}

func (g Identifier) RepositoryPathFor() string {
	return filepath.Join(basePath, "git", string(g))
}

func (g Identifier) EnvironmentPathFor() string {
	return isolateContentPath(filepath.Join(basePath, "env", "contents"), string(g), "")
}

func (i Identifier) GitAccessPathFor(f Fingerprint, write bool) string {
	var access string
	if write {
		access = ".write"
	} else {
		access = ".read"
	}
	return isolateContentPath(filepath.Join(basePath, "access", "git"), string(i), f.ToShortName()+access)
}

func (i Identifier) SshAccessPathFor(f Fingerprint) string {
	return isolateContentPath(filepath.Join(basePath, "access", "gears", "ssh"), string(i), f.ToShortName())
}

func (i Identifier) PortDescriptionPathFor() string {
	return isolateContentPath(filepath.Join(basePath, "ports", "descriptions"), string(i), "")
}

func isolateContentPath(base, id, suffix string) string {
	var path string
	if suffix == "" {
		path = filepath.Join(base, id[0:2])
		suffix = id
	} else {
		path = filepath.Join(base, id[0:2], id)
	}
	// fail silently, require startup to set paths, let consumers
	// handle directory not found errors
	os.MkdirAll(path, 0770)
	return filepath.Join(path, suffix)
}

type Fingerprint []byte

func (f Fingerprint) ToShortName() string {
	return strings.Trim(base64.URLEncoding.EncodeToString(f), "=")
}

func (f Fingerprint) PublicKeyPathFor() string {
	return isolateContentPath(filepath.Join(basePath, "keys", "public"), f.ToShortName(), "")
}

const basePath = "/var/lib/gears"
const GearBasePath = basePath

func VerifyDataPaths() error {
	for _, path := range []string{
		basePath,
		filepath.Join(basePath, "targets"),
		filepath.Join(basePath, "units"),
		filepath.Join(basePath, "slices"),
		filepath.Join(basePath, "git"),
		filepath.Join(basePath, "env", "contents"),
		filepath.Join(basePath, "access", "git", "read"),
		filepath.Join(basePath, "access", "git", "write"),
		filepath.Join(basePath, "access", "gears", "ssh"),
		filepath.Join(basePath, "keys", "public"),
		filepath.Join(basePath, "ports", "descriptions"),
		filepath.Join(basePath, "ports", "interfaces"),
	} {
		if err := checkPath(path, os.FileMode(0770), true); err != nil {
			return err
		}
	}
	return nil
}

func InitializeTargets() error {
	for _, target := range [][]string{
		[]string{"gear", "multi-user.target"},
	} {
		name, wants := target[0], target[1]
		path := filepath.Join(basePath, "targets", name+".target")
		unit, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
		if os.IsExist(err) {
			continue
		} else if err != nil {
			return err
		}

		if errs := targetUnitTemplate.Execute(unit, targetUnit{name, wants}); errs != nil {
			log.Printf("gear: Unable to write target %s: %v", name, errs)
			continue
		}
		if errc := unit.Close(); errc != nil {
			log.Printf("gear: Unable to close target %s: %v", name, errc)
			continue
		}

		if _, errs := StartAndEnableUnit(SystemdConnection(), name+".target", path, "fail"); errs != nil {
			log.Printf("gear: Unable to start and enable target %s: %v", name, errs)
			continue
		}
	}
	return nil
}

func InitializeSlices() error {
	for _, name := range []string{
		"gear",
		"gear-small",
	} {
		path := filepath.Join(basePath, "slices", name+".slice")
		unit, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
		if os.IsExist(err) {
			continue
		} else if err != nil {
			return err
		}

		parent := "gear"
		if name == "gear" {
			parent = ""
		}
		if errs := sliceUnitTemplate.Execute(unit, sliceUnit{name, parent}); errs != nil {
			log.Printf("gear: Unable to write slice %s: %v", name, errs)
			continue
		}
		if errc := unit.Close(); errc != nil {
			log.Printf("gear: Unable to close slice %s: %v", name, errc)
			continue
		}

		if _, errs := StartAndEnableUnit(SystemdConnection(), name+".slice", path, "fail"); errs != nil {
			log.Printf("gear: Unable to start and enable slice %s: %v", name, errs)
			continue
		}
	}
	return nil
}

func checkPath(path string, mode os.FileMode, dir bool) error {
	stat, err := os.Lstat(path)
	if os.IsNotExist(err) && dir {
		err = os.MkdirAll(path, mode)
		stat, _ = os.Lstat(path)
	}
	if err != nil {
		return errors.New("gear: path (" + path + ") is not available: " + err.Error())
	}
	if stat.IsDir() != dir {
		return errors.New("gear: path (" + path + ") must be a directory instead of a file")
	}
	return nil
}

func DisableAllUnits() {
	for _, path := range []string{
		filepath.Join(basePath, "units"),
		filepath.Join(basePath, "slices"),
		filepath.Join(basePath, "targets"),
	} {
		filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				log.Printf("gear: Can't read %s: %v", p, err)
				return nil
			}
			if info.IsDir() {
				return nil
			}
			if _, err := SystemdConnection().DisableUnitFiles([]string{p}, false); err != nil {
				log.Printf("gear: Unable to disable %s: %+v", p, err)
			}
			return nil
		})
		if err := SystemdConnection().Reload(); err != nil {
			log.Printf("gear: systemd reload failed: %+v", err)
		}
	}
}
