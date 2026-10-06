package balemeow

import (
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Validate allocation-driving repeated fields before generated protobuf code
// can allocate millions of empty child structs inside a small hostile frame.
// Unknown fields are still accepted according to protobuf forward compatibility.
func preflightDecode(data []byte, descriptor protoreflect.MessageDescriptor) error {
	budget := 32768
	return inspectMessage(data, descriptor, 0, &budget)
}
func inspectMessage(data []byte, descriptor protoreflect.MessageDescriptor, depth int, budget *int) error {
	if depth >= 32 || *budget <= 0 {
		return protocolError()
	}
	*budget--
	counts := map[protoreflect.FieldNumber]int{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protocolError()
		}
		data = data[n:]
		if typ == protowire.StartGroupType || typ == protowire.EndGroupType {
			return protocolError()
		}
		length := protowire.ConsumeFieldValue(num, typ, data)
		if length < 0 {
			return protocolError()
		}
		field := descriptor.Fields().ByNumber(protoreflect.FieldNumber(num))
		if field != nil {
			if field.IsList() {
				counts[field.Number()]++
				if counts[field.Number()] > 4096 {
					return protocolError()
				}
			}
			if typ == protowire.BytesType {
				value, n := protowire.ConsumeBytes(data)
				if n < 0 {
					return protocolError()
				}
				if field.Kind() == protoreflect.MessageKind {
					if err := inspectMessage(value, field.Message(), depth+1, budget); err != nil {
						return err
					}
				} else if field.IsList() && field.IsPacked() {
					count := 0
					for len(value) > 0 {
						n := 0
						switch field.Kind() {
						case protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind, protoreflect.FloatKind:
							_, n = protowire.ConsumeFixed32(value)
						case protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind, protoreflect.DoubleKind:
							_, n = protowire.ConsumeFixed64(value)
						default:
							_, n = protowire.ConsumeVarint(value)
						}
						if n < 0 {
							return protocolError()
						}
						value = value[n:]
						count++
						if count > 4096 {
							return protocolError()
						}
					}
					counts[field.Number()] += count - 1
					if counts[field.Number()] > 4096 {
						return protocolError()
					}
				}
			}
		}
		data = data[length:]
	}
	return nil
}
