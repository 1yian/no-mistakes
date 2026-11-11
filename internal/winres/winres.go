package winres

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"path"
)

//go:embed no-mistakes.exe.manifest
var manifest []byte

func Manifest() []byte {
	return bytes.Clone(manifest)
}

const (
	TypeManifest   = 24     // RT_MANIFEST
	ManifestID     = 1      // CREATEPROCESS_MANIFEST_RESOURCE_ID
	ManifestLangID = 0x0409 // en-US
)

type Target struct {
	GOARCH        string
	Machine       uint16
	RelocADDR32NB uint16
}

var Targets = []Target{
	{GOARCH: "amd64", Machine: 0x8664, RelocADDR32NB: 0x0003}, // IMAGE_FILE_MACHINE_AMD64, IMAGE_REL_AMD64_ADDR32NB
	{GOARCH: "arm64", Machine: 0xaa64, RelocADDR32NB: 0x0002}, // IMAGE_FILE_MACHINE_ARM64, IMAGE_REL_ARM64_ADDR32NB
}

const ObjectDir = "cmd/no-mistakes"

func (t Target) ObjectPath() string {
	return path.Join(ObjectDir, "rsrc_windows_"+t.GOARCH+".syso")
}

const (
	fileHeaderSize    = 20
	sectionHeaderSize = 40
	relocationSize    = 10
	directorySize     = 16
	directoryEntry    = 8
	dataEntrySize     = 16

	subdirectory    = 0x80000000
	nameDirOffset   = directorySize + directoryEntry
	langDirOffset   = 2 * (directorySize + directoryEntry)
	DataEntryOffset = 3 * (directorySize + directoryEntry)
	manifestOffset  = DataEntryOffset + dataEntrySize

	sectionCharacteristics = 0x40000040 // IMAGE_SCN_CNT_INITIALIZED_DATA | IMAGE_SCN_MEM_READ
	symbolClassStatic      = 3          // IMAGE_SYM_CLASS_STATIC
)

func (t Target) Object() []byte {
	le := binary.LittleEndian

	var rsrc []byte
	directory := func(id, offset uint32) {
		rsrc = le.AppendUint32(rsrc, 0) // Characteristics
		rsrc = le.AppendUint32(rsrc, 0) // TimeDateStamp
		rsrc = le.AppendUint16(rsrc, 0) // MajorVersion
		rsrc = le.AppendUint16(rsrc, 0) // MinorVersion
		rsrc = le.AppendUint16(rsrc, 0) // NumberOfNamedEntries
		rsrc = le.AppendUint16(rsrc, 1) // NumberOfIdEntries
		rsrc = le.AppendUint32(rsrc, id)
		rsrc = le.AppendUint32(rsrc, offset)
	}
	directory(TypeManifest, subdirectory|nameDirOffset)
	directory(ManifestID, subdirectory|langDirOffset)
	directory(ManifestLangID, DataEntryOffset)
	// The RVA field holds the manifest's section offset as the relocation's
	// addend; the linker adds the final .rsrc section's virtual address.
	rsrc = le.AppendUint32(rsrc, manifestOffset)
	rsrc = le.AppendUint32(rsrc, uint32(len(manifest)))
	rsrc = le.AppendUint32(rsrc, 0) // CodePage
	rsrc = le.AppendUint32(rsrc, 0) // Reserved
	rsrc = append(rsrc, manifest...)
	for len(rsrc)%8 != 0 {
		rsrc = append(rsrc, 0)
	}

	rawDataOffset := uint32(fileHeaderSize + sectionHeaderSize)
	relocationsOffset := rawDataOffset + uint32(len(rsrc))
	symbolTableOffset := relocationsOffset + relocationSize

	var out []byte
	// COFF file header.
	out = le.AppendUint16(out, t.Machine)
	out = le.AppendUint16(out, 1) // NumberOfSections
	out = le.AppendUint32(out, 0) // TimeDateStamp
	out = le.AppendUint32(out, symbolTableOffset)
	out = le.AppendUint32(out, 1) // NumberOfSymbols
	out = le.AppendUint16(out, 0) // SizeOfOptionalHeader
	out = le.AppendUint16(out, 0) // Characteristics
	// .rsrc section header.
	out = append(out, ".rsrc\x00\x00\x00"...)
	out = le.AppendUint32(out, 0) // VirtualSize
	out = le.AppendUint32(out, 0) // VirtualAddress
	out = le.AppendUint32(out, uint32(len(rsrc)))
	out = le.AppendUint32(out, rawDataOffset)
	out = le.AppendUint32(out, relocationsOffset)
	out = le.AppendUint32(out, 0) // PointerToLinenumbers
	out = le.AppendUint16(out, 1) // NumberOfRelocations
	out = le.AppendUint16(out, 0) // NumberOfLinenumbers
	out = le.AppendUint32(out, sectionCharacteristics)
	out = append(out, rsrc...)
	// The data entry's RVA, relative to symbol 0 (the .rsrc section).
	out = le.AppendUint32(out, DataEntryOffset)
	out = le.AppendUint32(out, 0) // SymbolTableIndex
	out = le.AppendUint16(out, t.RelocADDR32NB)
	// Symbol 0: the .rsrc section symbol.
	out = append(out, ".rsrc\x00\x00\x00"...)
	out = le.AppendUint32(out, 0) // Value
	out = le.AppendUint16(out, 1) // SectionNumber
	out = le.AppendUint16(out, 0) // Type
	out = append(out, symbolClassStatic, 0)
	// Empty string table: just its own 4-byte size.
	out = le.AppendUint32(out, 4)
	return out
}
