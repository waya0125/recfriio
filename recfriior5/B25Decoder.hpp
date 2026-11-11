// $Id$
// arib25

#ifndef _B25_DECODER_H_
#define _B25_DECODER_H_

#include "error.hpp"
#include <stdint.h>

/**
 * B25 decode error
 */
class b25_error : public traceable_error {
public:
	/**
	 * コンストラクタ
	 * @param __arg エラーメッセージ
	 */
	b25_error(const std::string&  __arg) : traceable_error(__arg) {};
};

#ifndef _REAL_B25_
	typedef void ARIB_STD_B25;
	typedef void B_CAS_CARD;
#endif /* !defined(_REAL_B25_) */

/**
 * Multi2デコーダ
 */
class B25Decoder {
public:
	/**
	 * コンストラクタ
	 */
	B25Decoder() : round(4), strip(false), emm(false), b25(NULL), bcas(NULL), cardTransport(NULL) {
	};
	
	/**
	 * デストラクタ
	 */
	virtual ~B25Decoder() noexcept {
		close();
	};
	
	/**
	 * Multi2のround数(default = 4)を設定する。
	 * @param paramRound Multi2のround数
	 */
	void setRound(int32_t paramRound) noexcept {
		round = paramRound;
	};
	
	/**
	 * nullパケットを削除するかを設定する。
	 * @param paramStrip trueの場合nullパケットを削除する
	 */
	void setStrip(bool paramStrip) noexcept {
		strip = paramStrip;
	};
	
	/**
	 * EMMの処理を行なうかを設定する。
	 * @param paramEmm trueの場合EMMの処理を行なう
	 */
	void setEmmProcess(bool paramEmm) noexcept {
		emm = paramEmm;
	};
	
	/**
	 * リソースを開放する。
	 */
	void close() noexcept;
	
	/**
	 * デコーダの初期化を行なう。
	 * @exception b25_error 初期化エラー
	 */
	void open();
	
	/**
	 * デコーダへ入力ストリームの完了を伝える。
	 * @exception b25_error エラー
	 */
	void flush();
	
	/**
	 * デコーダへストリームを入力する。
	 * @param buf データ
	 * @param len データの長さ
	 * @exception b25_error エラー
	 */
	void put(const uint8_t *buf, int32_t len);
	
	/**
	 * デコーダからストリームを取得する。
	 * @param bufp データを入れるポインタへのポインタ
	 * @return データの長さ
	 * @exception b25_error エラー
	 */
	int32_t get(const uint8_t **bufp);
	
protected:
	/** Multi2のround数(default = 4) */
	int32_t round;
	/** nullパケットを削除するか? */
	bool strip;
	/** EMMの処理を行なうか? */
	bool emm;
	
	/** B25 */
	ARIB_STD_B25 *b25;
	/** BCAS */
	B_CAS_CARD   *bcas;
	/** 内蔵スロットのやり取りの口（B_CAS_CARD_TRANSPORT*）。NULL なら PC/SC */
	const void   *cardTransport;

public:
	/**
	 * open() の前に呼ぶ。チューナー内蔵の B-CAS スロットを使う（NULL なら PC/SC のリーダー）。
	 */
	void setCardTransport(const void *transport) { cardTransport = transport; }
};

#endif /* !defined(_B25_H_) */
