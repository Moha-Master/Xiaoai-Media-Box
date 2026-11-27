package xiaomi

import "testing"

func TestParseAlarmVolume(t *testing.T) {
	cases := []struct {
		in   string
		want int
		bad  bool
	}{
		{`{ "volume": 17 }`, 17, false},
		{`{"volume":0}`, 0, false},
		{`{"volume":100}`, 100, false},
		{`{"volume":150}`, 0, true}, // 越界：设备回读异常
		{`{"volume":-1}`, 0, true},
		{`not json`, 0, true},
	}
	for _, c := range cases {
		got, err := parseAlarmVolume(c.in)
		if c.bad {
			if err == nil {
				t.Fatalf("%q 应该报错，得到 %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q 不该报错：%v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%q => %d，期望 %d", c.in, got, c.want)
		}
	}
}
