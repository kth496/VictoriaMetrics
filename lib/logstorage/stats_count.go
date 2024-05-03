package logstorage

import (
	"slices"
	"strconv"
	"unsafe"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/logger"
)

type statsCount struct {
	fields       []string
	containsStar bool
}

func (sc *statsCount) String() string {
	return "count(" + fieldNamesString(sc.fields) + ")"
}

func (sc *statsCount) neededFields() []string {
	return getFieldsIgnoreStar(sc.fields)
}

func (sc *statsCount) newStatsProcessor() (statsProcessor, int) {
	scp := &statsCountProcessor{
		sc: sc,
	}
	return scp, int(unsafe.Sizeof(*scp))
}

type statsCountProcessor struct {
	sc *statsCount

	rowsCount uint64
}

func (scp *statsCountProcessor) updateStatsForAllRows(br *blockResult) int {
	fields := scp.sc.fields
	if scp.sc.containsStar {
		// Fast path - unconditionally count all the columns.
		scp.rowsCount += uint64(len(br.timestamps))
		return 0
	}
	if len(fields) == 1 {
		// Fast path for count(single_column)
		c := br.getColumnByName(fields[0])
		if c.isConst {
			if c.encodedValues[0] != "" {
				scp.rowsCount += uint64(len(br.timestamps))
			}
			return 0
		}
		if c.isTime {
			scp.rowsCount += uint64(len(br.timestamps))
			return 0
		}
		switch c.valueType {
		case valueTypeString:
			for _, v := range c.encodedValues {
				if v != "" {
					scp.rowsCount++
				}
			}
			return 0
		case valueTypeDict:
			zeroDictIdx := slices.Index(c.dictValues, "")
			if zeroDictIdx < 0 {
				scp.rowsCount += uint64(len(br.timestamps))
				return 0
			}
			for _, v := range c.encodedValues {
				if int(v[0]) != zeroDictIdx {
					scp.rowsCount++
				}
			}
			return 0
		case valueTypeUint8, valueTypeUint16, valueTypeUint32, valueTypeUint64, valueTypeFloat64, valueTypeIPv4, valueTypeTimestampISO8601:
			scp.rowsCount += uint64(len(br.timestamps))
			return 0
		default:
			logger.Panicf("BUG: unknown valueType=%d", c.valueType)
			return 0
		}
	}

	// Slow path - count rows containing at least a single non-empty value for the fields enumerated inside count().
	bm := getBitmap(len(br.timestamps))
	defer putBitmap(bm)

	bm.setBits()
	for _, f := range fields {
		c := br.getColumnByName(f)
		if c.isConst {
			if c.encodedValues[0] != "" {
				scp.rowsCount += uint64(len(br.timestamps))
				return 0
			}
			continue
		}
		if c.isTime {
			scp.rowsCount += uint64(len(br.timestamps))
			return 0
		}
		switch c.valueType {
		case valueTypeString:
			bm.forEachSetBit(func(i int) bool {
				return c.encodedValues[i] == ""
			})
		case valueTypeDict:
			if !slices.Contains(c.dictValues, "") {
				scp.rowsCount += uint64(len(br.timestamps))
				return 0
			}
			bm.forEachSetBit(func(i int) bool {
				dictIdx := c.encodedValues[i][0]
				return c.dictValues[dictIdx] == ""
			})
		case valueTypeUint8, valueTypeUint16, valueTypeUint32, valueTypeUint64, valueTypeFloat64, valueTypeIPv4, valueTypeTimestampISO8601:
			scp.rowsCount += uint64(len(br.timestamps))
			return 0
		default:
			logger.Panicf("BUG: unknown valueType=%d", c.valueType)
			return 0
		}
	}

	scp.rowsCount += uint64(len(br.timestamps))
	bm.forEachSetBit(func(i int) bool {
		scp.rowsCount--
		return true
	})
	return 0
}

func (scp *statsCountProcessor) updateStatsForRow(br *blockResult, rowIdx int) int {
	fields := scp.sc.fields
	if scp.sc.containsStar {
		// Fast path - unconditionally count the given column
		scp.rowsCount++
		return 0
	}
	if len(fields) == 1 {
		// Fast path for count(single_column)
		c := br.getColumnByName(fields[0])
		if c.isConst {
			if c.encodedValues[0] != "" {
				scp.rowsCount++
			}
			return 0
		}
		if c.isTime {
			scp.rowsCount++
			return 0
		}
		switch c.valueType {
		case valueTypeString:
			if v := c.encodedValues[rowIdx]; v != "" {
				scp.rowsCount++
			}
			return 0
		case valueTypeDict:
			dictIdx := c.encodedValues[rowIdx][0]
			if v := c.dictValues[dictIdx]; v != "" {
				scp.rowsCount++
			}
			return 0
		case valueTypeUint8, valueTypeUint16, valueTypeUint32, valueTypeUint64, valueTypeFloat64, valueTypeIPv4, valueTypeTimestampISO8601:
			scp.rowsCount++
			return 0
		default:
			logger.Panicf("BUG: unknown valueType=%d", c.valueType)
			return 0
		}
	}

	// Slow path - count the row at rowIdx if at least a single field enumerated inside count() is non-empty
	for _, f := range fields {
		c := br.getColumnByName(f)
		if v := c.getValueAtRow(br, rowIdx); v != "" {
			scp.rowsCount++
			return 0
		}
	}
	return 0
}

func (scp *statsCountProcessor) mergeState(sfp statsProcessor) {
	src := sfp.(*statsCountProcessor)
	scp.rowsCount += src.rowsCount
}

func (scp *statsCountProcessor) finalizeStats() string {
	return strconv.FormatUint(scp.rowsCount, 10)
}

func parseStatsCount(lex *lexer) (*statsCount, error) {
	fields, err := parseFieldNamesForStatsFunc(lex, "count")
	if err != nil {
		return nil, err
	}
	sc := &statsCount{
		fields:       fields,
		containsStar: slices.Contains(fields, "*"),
	}
	return sc, nil
}

func getFieldsIgnoreStar(fields []string) []string {
	var result []string
	for _, f := range fields {
		if f != "*" {
			result = append(result, f)
		}
	}
	return result
}
