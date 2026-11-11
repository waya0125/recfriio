// $Id$
// Thread間データ転送用 リングバッファ(FIFO)管理クラス

#ifndef _RINGBUF_HPP_
#define _RINGBUF_HPP_

#include <iostream>
#include <exception>
#include <stdexcept>
#include <cstddef>
#include <chrono>
#include <condition_variable>
#include <mutex>

// バッファのオーバーフロー時に投げられます。
class overflow_error : public std::runtime_error {
public:
	overflow_error(const std::string&  __arg) : std::runtime_error(__arg) {
	};
};

// 割り込まれた時に投げられます。
class interrupted_error : public std::runtime_error {
public:
	interrupted_error(const std::string&  __arg) : std::runtime_error(__arg) {
	};
};

// cppは素人なのでコピーとか全く考えていません。
template <class T> class RingBuf {
public:
	// コンストラクタ/デストラクタ
	RingBuf(size_t size);
	virtual ~RingBuf();
	
	// バッファオーバーフローが起こっている。
	bool isOverflow();
	
	// 書込み中Node数
	unsigned int getWaitCount();
	
	// 読込み可能Node数
	unsigned int getReadyCount();
	
	// 次の書込み側のポインタを取得する。
	// timeout_msec以上setReadyされなかった場合 timeout_handler(T*) が呼ばれる。
	// Timeout処理はバッファからの読み出し時に先頭の要素に対して行われる。
	T *getPushPtr(unsigned int timeout_msec, void (*timeout_handler)(T *));
	
	// 書込み完了通知
	void setReady(T *node);
	
	// 次の読込み側ポインタを取得する。
	const T *getPopPtr();
	
	// 次の読込み側ポインタを取得する(タイムアウト付)
	// timeout時NULL
	const T *getPopPtr(unsigned int timeout_msec);
	
	// 次の読込み側ポインタを取得する(ブロックしない)
	// 現在読込めない場合はNULL
	const T *peekPopPtr();
	
	// 入力待ち強制終了用 割り込む。
	void interrupt();
	
protected:
	
	// peekPopPtrのmutexをロックしない版
	const T *peekPopPtrWithoutLock();
	
	// 時刻は単調時計で持つ（壁時計を合わせても待ち時間が狂わないように）。
	typedef std::chrono::steady_clock clock;
	
	// リングバッファのノード
	struct Node {
		Node *next; // 次
		clock::time_point timeout; // タイムアウト時刻
		void (*timeout_handler)(T *); // タイムアウトハンドラ
		bool is_wait;  // 書込み中?
		bool is_ready; // 読込み可能か？
		T data;     // データ本体
	};
	
	bool is_overflow;
	bool is_interrupted;
	
	Node *node_top;
	Node *node_push;
	Node *node_pop;
	
	std::mutex              mutex;
	std::condition_variable cond;

	unsigned int wait_count;
	unsigned int ready_count;
};

template <class T>
RingBuf<T>::RingBuf(size_t size) {
	is_overflow = false;
	is_interrupted = false;
	
	node_top = node_push = node_pop = new Node[size];
	wait_count = ready_count = 0;
	
	// リングバッファ設定
	for (uint32_t i = 0; i < size; i++) {
		node_top[i].next = (i+1 == size) ? &(node_top[0]) : &(node_top[i+1]);
		node_top[i].timeout = clock::now();
		node_top[i].timeout_handler = NULL;
		node_top[i].is_ready = false;
		node_top[i].is_wait  = false;
	}
}
template <class T>
RingBuf<T>::~RingBuf() {
	delete[] node_top;
}

// バッファオーバーフローが起こっている。
template <class T>
bool
RingBuf<T>::isOverflow() {
	std::unique_lock<std::mutex> lock(mutex);
	return is_overflow;
}

// 書込み中Node数
template <class T>
unsigned int 
RingBuf<T>::getWaitCount() {
	std::unique_lock<std::mutex> lock(mutex);
	return wait_count - ready_count;
}

// 読込み可能Node数
template <class T>
unsigned int 
RingBuf<T>::getReadyCount() {
	std::unique_lock<std::mutex> lock(mutex);
	return ready_count;
}

// 次の書込み側のポインタを取得する。
template <class T>
T *
RingBuf<T>::getPushPtr(unsigned int timeout_msec, void (*timeout_handler)(T *)) {
	std::unique_lock<std::mutex> lock(mutex);
	if (is_overflow) {
		throw overflow_error("RingBuf was overflowed.");
	}
	
	// Overflowチェック
	if (node_push->next == node_pop || node_push->next->next == node_pop) {
		is_overflow = true;
		throw overflow_error("RingBuf is overflow.");
	}
	
	T *data = &(node_push->data);
	node_push->timeout = clock::now() + std::chrono::milliseconds(timeout_msec);
	node_push->timeout_handler = timeout_handler;
	node_push->is_wait = true;
	wait_count++;
	
	node_push = node_push->next;
	
	return data;
	
}

// 書込み完了通知
template <class T>
void
RingBuf<T>::setReady(T *node) {
	std::unique_lock<std::mutex> lock(mutex);
	if (is_overflow) {
		throw overflow_error("RingBuf was overflowed.");
	}
	
	Node *real_node = (Node *)((char *)node - offsetof(Node, data));
	if (real_node->is_wait) {
		real_node->is_ready = true;
		ready_count++;

	} else {
		// TODO: なぜかTimeoutしたURBをキャンセルするとここにくる。
		// std::cerr << "SetReady called for not waiting node." << std::endl;
	}
	cond.notify_one();
}

// 次の読込み側ポインタを取得する。
template <class T>
const T *
RingBuf<T>::getPopPtr() {
	const T *data = NULL;
	while(true) {
		data = getPopPtr(100);
		if (NULL != data) {
			break; // read.
		}
	}
	
	return data;
}

// 次の読込み側ポインタを取得する(タイムアウト付)
// timeout時NULL
template <class T>
const T *
RingBuf<T>::getPopPtr(unsigned int timeout_msec) {
	std::unique_lock<std::mutex> lock(mutex);
	if (is_overflow) {
		throw overflow_error("RingBuf was overflowed.");
	}
	
	const T *data = NULL;
	while(true) {
		if (is_interrupted) {
			throw interrupted_error("inturrupted!");
		}
		
		data = peekPopPtrWithoutLock();
		if (NULL != data) {
			break; // read.
		}
		
		// Timeout時間の厳密性を考えるのが面倒なので適当に処理する。
		// timeout_msecで待つ
		if (cond.wait_for(lock, std::chrono::milliseconds(timeout_msec)) == std::cv_status::timeout) {
			return NULL; // Timeout;
		}
	}
	
	return data;
}

// 次の読込み側ポインタを取得する(ブロックしない)
// 現在読込めない場合はNULL
template <class T>
const T *
RingBuf<T>::peekPopPtr() {
	std::unique_lock<std::mutex> lock(mutex);
	if (is_overflow) {
		throw overflow_error("RingBuf was overflowed.");
	}
	
	return peekPopPtrWithoutLock();
}

// 割り込む
template <class T>
void
RingBuf<T>::interrupt() {
	std::unique_lock<std::mutex> lock(mutex);	
	is_interrupted = true;
	cond.notify_one();
}

// 既にロックされている状態で読込めるか確認
template <class T>
const T *
RingBuf<T>::peekPopPtrWithoutLock() {
	if (is_overflow) {
		throw overflow_error("RingBuf was overflowed.");
	}
	if (node_pop != node_push) {
		if (node_pop->is_ready) {
			// リセットして返す。
			node_pop->timeout = clock::now();
			node_pop->timeout_handler = NULL;
			node_pop->is_ready = false;
			node_pop->is_wait  = false;
			const T *data = &(node_pop->data);
			
			node_pop = node_pop->next;
			ready_count--;
			wait_count--;
			
			return data;
		}
		
		if (node_pop->timeout <= clock::now()) {
			// Timeoutを過ぎている。
			if (node_pop->timeout_handler) {
				// timeout_handler呼出し
				node_pop->timeout_handler(&(node_pop->data));
			}
			
			// リセットして次へ。
			node_pop->timeout = clock::now();
			node_pop->timeout_handler = NULL;
			node_pop->is_ready = false;
			node_pop->is_wait  = false;
			
			node_pop = node_pop->next;
			wait_count--;
			
			return peekPopPtrWithoutLock();
		}
	}
	
	return NULL;
}

#endif
