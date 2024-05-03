package logstorage

import (
	"slices"
	"strconv"
	"unsafe"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/bytesutil"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/encoding"
)

type statsUniq struct {
	fields       []string
	containsStar bool
}

func (su *statsUniq) String() string {
	return "uniq(" + fieldNamesString(su.fields) + ")"
}

func (su *statsUniq) neededFields() []string {
	return su.fields
}

func (su *statsUniq) newStatsProcessor() (statsProcessor, int) {
	sup := &statsUniqProcessor{
		su: su,

		m: make(map[string]struct{}),
	}
	return sup, int(unsafe.Sizeof(*sup))
}

type statsUniqProcessor struct {
	su *statsUniq

	m map[string]struct{}

	columnValues [][]string
	keyBuf       []byte
}

func (sup *statsUniqProcessor) updateStatsForAllRows(br *blockResult) int {
	fields := sup.su.fields
	m := sup.m

	stateSizeIncrease := 0
	if sup.su.containsStar {
		// Count unique rows
		columns := br.getColumns()
		keyBuf := sup.keyBuf[:0]
		for i := range br.timestamps {
			seenKey := true
			for _, c := range columns {
				values := c.getValues(br)
				if i == 0 || values[i-1] != values[i] {
					seenKey = false
					break
				}
			}
			if seenKey {
				// This key has been already counted.
				continue
			}

			allEmptyValues := true
			keyBuf = keyBuf[:0]
			for _, c := range columns {
				v := c.getValueAtRow(br, i)
				if v != "" {
					allEmptyValues = false
				}
				// Put column name into key, since every block can contain different set of columns for '*' selector.
				keyBuf = encoding.MarshalBytes(keyBuf, bytesutil.ToUnsafeBytes(c.name))
				keyBuf = encoding.MarshalBytes(keyBuf, bytesutil.ToUnsafeBytes(v))
			}
			if allEmptyValues {
				// Do not count empty values
				continue
			}
			if _, ok := m[string(keyBuf)]; !ok {
				m[string(keyBuf)] = struct{}{}
				stateSizeIncrease += len(keyBuf) + int(unsafe.Sizeof(""))
			}
		}
		sup.keyBuf = keyBuf
		return stateSizeIncrease
	}
	if len(fields) == 1 {
		// Fast path for a single column.
		// The unique key is formed as "<is_time> <value_type>? <encodedValue>",
		// where <value_type> is skipped if <is_time> == 1.
		// This guarantees that keys do not clash for different column types across blocks.
		c := br.getColumnByName(fields[0])
		if c.isTime {
			// Count unique br.timestamps
			timestamps := br.timestamps
			keyBuf := sup.keyBuf[:0]
			for i, timestamp := range timestamps {
				if i > 0 && timestamps[i-1] == timestamps[i] {
					// This timestamp has been already counted.
					continue
				}
				keyBuf = append(keyBuf[:0], 1)
				keyBuf = encoding.MarshalInt64(keyBuf, timestamp)
				if _, ok := m[string(keyBuf)]; !ok {
					m[string(keyBuf)] = struct{}{}
					stateSizeIncrease += len(keyBuf) + int(unsafe.Sizeof(""))
				}
			}
			sup.keyBuf = keyBuf
			return stateSizeIncrease
		}
		if c.isConst {
			// count unique const values
			v := c.encodedValues[0]
			if v == "" {
				// Do not count empty values
				return stateSizeIncrease
			}
			keyBuf := sup.keyBuf[:0]
			keyBuf = append(keyBuf[:0], 0, byte(valueTypeString))
			keyBuf = append(keyBuf, v...)
			if _, ok := m[string(keyBuf)]; !ok {
				m[string(keyBuf)] = struct{}{}
				stateSizeIncrease += len(keyBuf) + int(unsafe.Sizeof(""))
			}
			sup.keyBuf = keyBuf
			return stateSizeIncrease
		}
		if c.valueType == valueTypeDict {
			// count unique non-zero c.dictValues
			keyBuf := sup.keyBuf[:0]
			for i, v := range c.dictValues {
				if v == "" {
					// Do not count empty values
					continue
				}
				keyBuf = append(keyBuf[:0], 0, byte(valueTypeDict))
				keyBuf = append(keyBuf, byte(i))
				if _, ok := m[string(keyBuf)]; !ok {
					m[string(keyBuf)] = struct{}{}
					stateSizeIncrease += len(keyBuf) + int(unsafe.Sizeof(""))
				}
			}
			sup.keyBuf = keyBuf
			return stateSizeIncrease
		}

		// Count unique values across encodedValues
		encodedValues := c.getEncodedValues(br)
		isStringValueType := c.valueType == valueTypeString
		keyBuf := sup.keyBuf[:0]
		for i, v := range encodedValues {
			if isStringValueType && v == "" {
				// Do not count empty values
				continue
			}
			if i > 0 && encodedValues[i-1] == v {
				// This value has been already counted.
				continue
			}
			keyBuf = append(keyBuf[:0], 0, byte(c.valueType))
			keyBuf = append(keyBuf, v...)
			if _, ok := m[string(keyBuf)]; !ok {
				m[string(keyBuf)] = struct{}{}
				stateSizeIncrease += len(keyBuf) + int(unsafe.Sizeof(""))
			}
		}
		keyBuf = sup.keyBuf
		return stateSizeIncrease
	}

	// Slow path for multiple columns.

	// Pre-calculate column values for byFields in order to speed up building group key in the loop below.
	columnValues := sup.columnValues[:0]
	for _, f := range fields {
		c := br.getColumnByName(f)
		values := c.getValues(br)
		columnValues = append(columnValues, values)
	}
	sup.columnValues = columnValues

	keyBuf := sup.keyBuf[:0]
	for i := range br.timestamps {
		seenKey := true
		for _, values := range columnValues {
			if i == 0 || values[i-1] != values[i] {
				seenKey = false
				break
			}
		}
		if seenKey {
			continue
		}

		allEmptyValues := true
		keyBuf = keyBuf[:0]
		for _, values := range columnValues {
			v := values[i]
			if v != "" {
				allEmptyValues = false
			}
			keyBuf = encoding.MarshalBytes(keyBuf, bytesutil.ToUnsafeBytes(v))
		}
		if allEmptyValues {
			// Do not count empty values
			continue
		}
		if _, ok := m[string(keyBuf)]; !ok {
			m[string(keyBuf)] = struct{}{}
			stateSizeIncrease += len(keyBuf) + int(unsafe.Sizeof(""))
		}
	}
	sup.keyBuf = keyBuf
	return stateSizeIncrease
}

func (sup *statsUniqProcessor) updateStatsForRow(br *blockResult, rowIdx int) int {
	fields := sup.su.fields
	m := sup.m

	stateSizeIncrease := 0
	if sup.su.containsStar {
		// Count unique rows
		allEmptyValues := true
		keyBuf := sup.keyBuf[:0]
		for _, c := range br.getColumns() {
			v := c.getValueAtRow(br, rowIdx)
			if v != "" {
				allEmptyValues = false
			}
			// Put column name into key, since every block can contain different set of columns for '*' selector.
			keyBuf = encoding.MarshalBytes(keyBuf, bytesutil.ToUnsafeBytes(c.name))
			keyBuf = encoding.MarshalBytes(keyBuf, bytesutil.ToUnsafeBytes(v))
		}
		sup.keyBuf = keyBuf

		if allEmptyValues {
			// Do not count empty values
			return stateSizeIncrease
		}
		if _, ok := m[string(keyBuf)]; !ok {
			m[string(keyBuf)] = struct{}{}
			stateSizeIncrease += len(keyBuf) + int(unsafe.Sizeof(""))
		}
		return stateSizeIncrease
	}
	if len(fields) == 1 {
		// Fast path for a single column.
		// The unique key is formed as "<is_time> <value_type>? <encodedValue>",
		// where <value_type> is skipped if <is_time> == 1.
		// This guarantees that keys do not clash for different column types across blocks.
		c := br.getColumnByName(fields[0])
		if c.isTime {
			// Count unique br.timestamps
			keyBuf := sup.keyBuf[:0]
			keyBuf = append(keyBuf[:0], 1)
			keyBuf = encoding.MarshalInt64(keyBuf, br.timestamps[rowIdx])
			if _, ok := m[string(keyBuf)]; !ok {
				m[string(keyBuf)] = struct{}{}
				stateSizeIncrease += len(keyBuf) + int(unsafe.Sizeof(""))
			}
			sup.keyBuf = keyBuf
			return stateSizeIncrease
		}
		if c.isConst {
			// count unique const values
			v := c.encodedValues[0]
			if v == "" {
				// Do not count empty values
				return stateSizeIncrease
			}
			keyBuf := sup.keyBuf[:0]
			keyBuf = append(keyBuf[:0], 0, byte(valueTypeString))
			keyBuf = append(keyBuf, v...)
			if _, ok := m[string(keyBuf)]; !ok {
				m[string(keyBuf)] = struct{}{}
				stateSizeIncrease += len(keyBuf) + int(unsafe.Sizeof(""))
			}
			sup.keyBuf = keyBuf
			return stateSizeIncrease
		}
		if c.valueType == valueTypeDict {
			// count unique non-zero c.dictValues
			dictIdx := c.encodedValues[rowIdx][0]
			if c.dictValues[dictIdx] == "" {
				// Do not count empty values
				return stateSizeIncrease
			}
			keyBuf := sup.keyBuf[:0]
			keyBuf = append(keyBuf[:0], 0, byte(valueTypeDict))
			keyBuf = append(keyBuf, dictIdx)
			if _, ok := m[string(keyBuf)]; !ok {
				m[string(keyBuf)] = struct{}{}
				stateSizeIncrease += len(keyBuf) + int(unsafe.Sizeof(""))
			}
			sup.keyBuf = keyBuf
			return stateSizeIncrease
		}

		// Count unique values for the given rowIdx
		encodedValues := c.getEncodedValues(br)
		v := encodedValues[rowIdx]
		if c.valueType == valueTypeString && v == "" {
			// Do not count empty values
			return stateSizeIncrease
		}
		keyBuf := sup.keyBuf[:0]
		keyBuf = append(keyBuf[:0], 0, byte(c.valueType))
		keyBuf = append(keyBuf, v...)
		if _, ok := m[string(keyBuf)]; !ok {
			m[string(keyBuf)] = struct{}{}
			stateSizeIncrease += len(keyBuf) + int(unsafe.Sizeof(""))
		}
		keyBuf = sup.keyBuf
		return stateSizeIncrease
	}

	// Slow path for multiple columns.
	allEmptyValues := true
	keyBuf := sup.keyBuf[:0]
	for _, f := range fields {
		c := br.getColumnByName(f)
		v := c.getValueAtRow(br, rowIdx)
		if v != "" {
			allEmptyValues = false
		}
		keyBuf = encoding.MarshalBytes(keyBuf, bytesutil.ToUnsafeBytes(v))
	}
	sup.keyBuf = keyBuf

	if allEmptyValues {
		// Do not count empty values
		return stateSizeIncrease
	}
	if _, ok := m[string(keyBuf)]; !ok {
		m[string(keyBuf)] = struct{}{}
		stateSizeIncrease += len(keyBuf) + int(unsafe.Sizeof(""))
	}
	return stateSizeIncrease
}

func (sup *statsUniqProcessor) mergeState(sfp statsProcessor) {
	src := sfp.(*statsUniqProcessor)
	m := sup.m
	for k := range src.m {
		if _, ok := m[k]; !ok {
			m[k] = struct{}{}
		}
	}
}

func (sup *statsUniqProcessor) finalizeStats() string {
	n := uint64(len(sup.m))
	return strconv.FormatUint(n, 10)
}

func parseStatsUniq(lex *lexer) (*statsUniq, error) {
	fields, err := parseFieldNamesForStatsFunc(lex, "uniq")
	if err != nil {
		return nil, err
	}
	su := &statsUniq{
		fields:       fields,
		containsStar: slices.Contains(fields, "*"),
	}
	return su, nil
}
