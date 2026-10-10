package balemeow

import (
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"google.golang.org/protobuf/encoding/protowire"
	"testing"
)

func TestDecodeRejectsRepeatedAllocationBombBeforeUnmarshal(t *testing.T) {
	data := []byte{}
	for i := 0; i < 4097; i++ {
		data = protowire.AppendTag(data, 1, protowire.BytesType)
		data = protowire.AppendBytes(data, nil)
	}
	target := &wire.HistoryResponse{}
	if decode(data, target) == nil {
		t.Fatal("excessive empty history accepted")
	}
	if len(target.History) != 0 {
		t.Fatal("generated decoder allocated records before preflight rejected")
	}
}
func TestDecodePackedScalarsBoundedAcrossSegments(t *testing.T) {
	fields := []byte{}
	for i := 0; i < 2; i++ {
		packed := []byte{}
		for j := 0; j < 3000; j++ {
			packed = protowire.AppendVarint(packed, 1)
		}
		fields = protowire.AppendTag(fields, 2, protowire.BytesType)
		fields = protowire.AppendBytes(fields, packed)
	}
	target := &wire.UpdateMessageDeleted{}
	if decode(fields, target) == nil || len(target.Rids) != 0 {
		t.Fatal("packed RID allocation was not bounded")
	}
}
func TestDecodeUnknownProto3FieldsStayCompatible(t *testing.T) {
	data := protowire.AppendTag(nil, 999, protowire.BytesType)
	data = protowire.AppendBytes(data, []byte("synthetic unknown field"))
	if decode(data, &wire.Empty{}) != nil {
		t.Fatal("ordinary unknown field rejected")
	}
	group := protowire.AppendTag(nil, 999, protowire.StartGroupType)
	if decode(group, &wire.Empty{}) == nil {
		t.Fatal("legacy groups are unsupported")
	}
}
