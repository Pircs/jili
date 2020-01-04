package bucket

import (
	"sync"
	"time"
)

// Bucket 会保留一部分的 token，仅供 Hurry 使用。
type Bucket interface {
	// Hurry 会先使用为自己保存的 token
	// 不够的时候，再 Wait token
	Hurry(count int64)

	// Wait 无法使用保留的 token
	Wait(count int64)
}

var now = time.Now
var sleep = time.Sleep

type bucket struct {
	reserving, normal *subBucket
}

// New 返回了 Bucket 接口的变量
// reserving 代表了 duration 期间保留给 Hurry 方法的 token 数量
// 请注意，reserving 的数量请尽量少一点。
// 因为，Hurry时候，如果需要用到 Wait 的话，
// 只能按照 (capacity-reserving)/duration 的速度，等待剩下的 token
func New(duration time.Duration, capacity, reserving int64) Bucket {
	start := now()
	return &bucket{
		reserving: newBasic(start, duration, reserving),
		normal:    newBasic(start, duration, capacity-reserving),
	}
}

var hurryQuickReturn = func() {}

func (b *bucket) Hurry(count int64) {
	if count <= 0 {
		hurryQuickReturn()
		return
	}
	debt := b.reserving.hurry(count, now())
	b.Wait(debt)
}

var waitQuickReturn = func() {}

func (b *bucket) Wait(count int64) {
	if count <= 0 {
		waitQuickReturn()
		return
	}
	dur := b.normal.wait(count, now())
	sleep(dur)
}

type subBucket struct {
	// 创建的时间
	start time.Time
	// 最大容量
	capacity int64
	// tick 的时长
	interval time.Duration
	// 每 tick 添加的数量
	quantum int64
	// mutex 保护以下属性
	sync.Mutex
	// 更新令牌的时间点
	tick int64
	// 普通令牌的数量
	available int64
}

// newBasic return basic pointer
func newBasic(start time.Time, duration time.Duration, capacity int64) *subBucket {
	if capacity <= 0 {
		panic("bucket's capacity should > 0")
	}
	//   rate
	// = capacity ÷ duration
	// = quantum ÷ interval
	// 由于此 bucket 的应用场景中，
	// duration 远大于 capacity 且
	// 极大概率 duration%capacity 等于 0
	// 所以，采用此方案求取 interval 和 quantum
	// 在其他场景下，还是要使用
	// https://github.com/juju/ratelimit/blob/f60b32039441cd828005f82f3a54aafd00bc9882/ratelimit.go#L104
	// 中使用的方法。
	d := gcd(int64(duration), capacity)
	interval, quantum := duration/time.Duration(d), capacity/d
	return &subBucket{
		start:     start,
		capacity:  capacity,
		interval:  interval,
		quantum:   quantum,
		available: capacity,
		tick:      0,
	}
}

func (b *subBucket) update(now time.Time) {
	lastTick, newTick := b.tick, b.time2tick(now)
	b.tick = newTick
	b.available += (newTick - lastTick) * b.quantum
	if b.available > b.capacity {
		b.available = b.capacity
	}
}

func (b *subBucket) consume(count int64) int64 {
	remain := b.available - count
	if remain < 0 {
		b.available = 0
		return -remain
	}
	b.available = remain
	return 0
}

// 当运行 needDuration 时，b.available 应该是 0
func (b *subBucket) needDuration(debt int64, now time.Time) time.Duration {
	if debt == 0 {
		return 0
	}
	b.available -= debt
	// +(b.quantum-1) 是为了到达 endTick 时， 一定有足够的 token
	endTick := b.tick + (debt+(b.quantum-1))/b.quantum
	endTime := b.start.Add(time.Duration(endTick) * b.interval)
	return endTime.Sub(now)
}

func (b *subBucket) hurry(count int64, now time.Time) int64 {
	b.Lock()
	defer b.Unlock()
	b.update(now)
	return b.consume(count)
}

func (b *subBucket) wait(count int64, now time.Time) time.Duration {
	b.Lock()
	defer b.Unlock()
	b.update(now)
	debt := b.consume(count)
	return b.needDuration(debt, now)
}

func (b *subBucket) time2tick(t time.Time) int64 {
	return int64(t.Sub(b.start) / b.interval)
}

func (b *subBucket) tick2Time() time.Time {
	return b.start.Add(b.interval * time.Duration(b.tick))
}

func gcd(m, n int64) int64 {
	if n == 0 {
		return m
	}
	return gcd(n, m%n)
}

type bucket2 struct {
	// 创建的时间
	start time.Time
	// 预留的 token 数量
	reserved int64
	// 普通的 token 数量
	normal int64
	// tick 的时长
	interval time.Duration
	// 每 tick 添加的数量
	quantum int64
	// mutex 保护以下属性
	sync.Mutex
	// 更新令牌的时间点
	tick int64
	// 的数量
	hToken, wToken int64
}

func newBucket2(duration time.Duration, capacity, reserved int64) *bucket2 {
	if !(0 <= reserved && reserved < capacity) {
		panic("bucket's parameter should 0 <= reserved < capacity")
	}
	//   rate
	// = capacity ÷ duration
	// = quantum ÷ interval
	// 由于此 bucket 的应用场景中，
	// duration 远大于 capacity 且
	// 极大概率 duration%capacity 等于 0
	// 所以，采用此方案求取 interval 和 quantum
	// 在其他场景下，还是要使用
	// https://github.com/juju/ratelimit/blob/f60b32039441cd828005f82f3a54aafd00bc9882/ratelimit.go#L104
	// 中使用的方法。
	d := gcd(int64(duration), capacity)
	interval, quantum := duration/time.Duration(d), capacity/d
	return &bucket2{
		start:    now(),
		reserved: reserved,
		normal:   capacity - reserved,
		interval: interval,
		quantum:  quantum,
		tick:     0,
		hToken:   reserved,
		wToken:   capacity - reserved,
	}
}

func (b *bucket2) Hurry(count int64) {
	if count <= 0 {
		return
	}
	b.Lock()
	nowTime := now()
	b.update(nowTime)
	debt := b.hTake(count)
	sleep(b.needTime(debt, nowTime))
	b.Unlock()
}

func (b *bucket2) Wait(count int64) {
	if count <= 0 {
		return
	}
	b.Lock()
	nowTime := now()
	b.update(nowTime)
	debt := b.wTake(count)
	sleep(b.needTime(debt, nowTime))
	b.Unlock()
}

func (b *bucket2) update(now time.Time) {
	lastTick, newTick := b.tick, b.time2tick(now)
	b.tick = newTick
	token := (newTick - lastTick) * b.quantum
	// 优先放在 hToken
	b.hToken += token
	if b.hToken <= b.reserved {
		return
	}
	// hToken 有盈余，再喂给 wToken
	b.wToken += b.hToken - b.reserved
	b.hToken = b.reserved
	if b.wToken <= b.normal {
		return
	}
	// wToken 还有盈余，再裁剪
	b.wToken = b.normal
}

// hTake 返回还需要 token 的数量
func (b *bucket2) hTake(count int64) int64 {
	allToken := b.hToken + b.wToken
	switch {
	// case count <= 0:
	// return 0
	case count <= b.hToken:
		b.hToken -= count
		return 0
	case count <= allToken:
		b.wToken -= count - b.hToken
		b.hToken = 0
		return 0
	default: // allToken < count
		// 理解 wToken 成为负数，是本模块的一个难点
		// 本函数在 Hurry 中被调用。
		// 如果运行到了此行，在 Hurry 中一定会 sleep 一段时间。
		// 那么在下一次 b.update 的以后，
		// 一定可以保证 b.hToken + b.wToken >= 0
		b.wToken -= count - b.hToken
		b.hToken = 0
		return -b.wToken
	}
}

// wTake 返回还需要 token 的数量
func (b *bucket2) wTake(count int64) int64 {
	if count <= b.wToken {
		b.wToken -= count
		return 0
	}
	// b.wToken < count
	// 理解 wToken 成为负数，是本模块的一个难点
	// 本函数在 Wait 中被调用。
	// 如果运行到了此行，在 Wait 中一定会 sleep 一段时间。
	// 那么在下一次 b.update 的以后，
	// 一定可以保证 b.hToken + b.wToken >= 0
	b.wToken -= count
	return -b.wToken
}

// need time to pay debt
func (b *bucket2) needTime(debt int64, now time.Time) time.Duration {
	if debt == 0 {
		return 0
	}
	// +(b.quantum-1) 是为了到达 endTick 时， 一定有足够的 token
	endTick := b.tick + (debt+(b.quantum-1))/b.quantum
	endTime := b.start.Add(time.Duration(endTick) * b.interval)
	return endTime.Sub(now)
}

func (b *bucket2) time2tick(t time.Time) int64 {
	return int64(t.Sub(b.start) / b.interval)
}

func (b *bucket2) tick2Time() time.Time {
	return b.start.Add(b.interval * time.Duration(b.tick))
}
