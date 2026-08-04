package grpcmcp

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// generateToolName picks the tool name format for a method. Short formats fall
// back to a longer form when the shorter one collides.
func generateToolName(shortNames bool, veryShortNames bool, hasShortCollision bool, hasVeryShortCollision bool, fullServiceName string, simpleServiceName string, methodName string) string {
	if veryShortNames && !hasVeryShortCollision {
		return methodName
	}
	if (shortNames || veryShortNames) && !hasShortCollision {
		return fmt.Sprintf("%v__%v", simpleServiceName, methodName)
	}
	return strings.ReplaceAll(fmt.Sprintf("%v__%v", fullServiceName, methodName), ".", "_")
}

// countNameCollisions counts how many services share each simple service name,
// and how many services define each method name. generateToolName uses both
// counts to decide when a short name must fall back to a longer form.
//
// The method scan skips streaming and filtered-out methods so that the counts
// match the set of tools that NewServer actually registers.
func countNameCollisions(cfg Config, files []protoreflect.FileDescriptor, servicesMap map[protoreflect.FullName]struct{}) (map[string]int, map[string]int) {
	simpleNameCount := map[string]int{}
	methodNameCount := map[string]int{}
	if !cfg.ShortNames && !cfg.VeryShortNames {
		return simpleNameCount, methodNameCount
	}
	for _, fd := range files {
		services := fd.Services()
		for i := range services.Len() {
			s := services.Get(i)
			if len(servicesMap) > 0 {
				if _, found := servicesMap[s.FullName()]; !found {
					continue
				}
			}
			simpleNameCount[string(s.Name())]++
			if !cfg.VeryShortNames {
				continue
			}
			methods := s.Methods()
			for j := range methods.Len() {
				m := methods.Get(j)
				if m.IsStreamingClient() || m.IsStreamingServer() {
					continue
				}
				if !cfg.MethodOption.matches(m) {
					continue
				}
				methodNameCount[string(m.Name())]++
			}
		}
	}
	return simpleNameCount, methodNameCount
}

// ParseMethodOptionFilter parses a `fieldNumber:value` or
// `fieldNumber:value1,value2` filter specification.
func ParseMethodOptionFilter(spec string) (*MethodOptionFilter, error) {
	fieldPart, valuePart, ok := strings.Cut(spec, ":")
	if !ok {
		return nil, fmt.Errorf("expecting the format fieldNumber:value or fieldNumber:value1,value2")
	}
	fieldNumber, err := strconv.ParseUint(fieldPart, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid field number: %w", err)
	}
	if fieldNumber == 0 {
		return nil, fmt.Errorf("field number must be greater than 0")
	}
	var values []uint64
	for _, valueStr := range strings.Split(valuePart, ",") {
		value, err := strconv.ParseUint(strings.TrimSpace(valueStr), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid value: %w", err)
		}
		values = append(values, value)
	}
	return &MethodOptionFilter{FieldNumber: uint32(fieldNumber), Values: values}, nil
}

// matches reports whether the method passes the filter. A nil filter accepts
// every method.
//
// The method options are inspected on the wire because the extension is not
// registered in this process. Only varint fields are compared, which covers the
// enum and integer options this filter targets.
func (f *MethodOptionFilter) matches(m protoreflect.MethodDescriptor) bool {
	if f == nil {
		return true
	}
	opts := m.Options()
	if opts == nil {
		return false
	}
	b, err := proto.Marshal(opts)
	if err != nil {
		return false
	}
	for len(b) > 0 {
		num, wtype, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		b = b[n:]
		if uint32(num) == f.FieldNumber && wtype == protowire.VarintType {
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return false
			}
			return slices.Contains(f.Values, v)
		}
		switch wtype {
		case protowire.VarintType:
			_, n = protowire.ConsumeVarint(b)
		case protowire.Fixed32Type:
			_, n = protowire.ConsumeFixed32(b)
		case protowire.Fixed64Type:
			_, n = protowire.ConsumeFixed64(b)
		case protowire.BytesType:
			_, n = protowire.ConsumeBytes(b)
		case protowire.StartGroupType:
			_, n = protowire.ConsumeGroup(num, b)
		default:
			return false
		}
		if n < 0 {
			return false
		}
		b = b[n:]
	}
	return false
}
