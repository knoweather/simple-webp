package vp8l

import (
	"reflect"
	"testing"
)

// TestHuffmanWriteRead 做一次编码端到解码端的往返：用 newHuffmanCode 生成树，
// writeHuffmanCode 写出位流，再用 readHuffmanCode 读回并比对解码表（counts/symbols），
// 以此验证写读两侧对规范码的解释一致。
// TestHuffmanWriteRead round-trips the encoder against the decoder: it builds a
// tree with newHuffmanCode, writes it with writeHuffmanCode, reads it back with
// readHuffmanCode and compares the decoded tables (counts/symbols), verifying
// that both sides interpret canonical codes identically.
func TestHuffmanWriteRead(t *testing.T) {
	cases := [][]uint32{
		{1},
		{0, 5},
		{3, 1, 1, 0, 2},
		{1, 1, 1, 1, 1, 1, 1, 1},
		{10, 5, 3, 1, 1, 1, 1, 1},
	}
	for ci, freq := range cases {
		h := newHuffmanCode(freq, 15)
		e := &encoder{}
		e.writeHuffmanCode(h)
		data := e.w.finish()
		br := newBitReader(data)
		got, err := readHuffmanCode(br, len(freq))
		if err != nil {
			t.Fatalf("case %d: %v (data % x)", ci, err, data)
		}
		if got.single >= 0 {
			if h.single && int(got.single) != indexOfNonZero(h.lengths) {
				t.Fatalf("case %d: single symbol %d, want %d", ci, got.single, indexOfNonZero(h.lengths))
			}
			continue
		}
		// 比对读回的符号表与写入前构建的码。
		// Compare the decoded symbol table against the written code.
		wantTree, err := buildHuffmanTree(h.lengths)
		if err != nil {
			t.Fatalf("case %d: %v", ci, err)
		}
		if !reflect.DeepEqual(got.counts, wantTree.counts) || !reflect.DeepEqual(got.symbols, wantTree.symbols) {
			t.Fatalf("case %d: got %v/%v, want %v/%v", ci, got.counts, got.symbols, wantTree.counts, wantTree.symbols)
		}
	}
}

// indexOfNonZero 返回第一个非零码长的下标，用于单符号树的断言；全零时返回 -1。
// indexOfNonZero returns the index of the first non-zero code length, used to
// assert the single-symbol tree case; it returns -1 when all lengths are zero.
func indexOfNonZero(l []uint32) int {
	for i, v := range l {
		if v > 0 {
			return i
		}
	}
	return -1
}
