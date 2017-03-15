package channels

import (
	"bytes"
	"errors"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
)

const (
	OP_CHECKLOCKTIMEVERIFY = 177
	OP_CHECKSEQUENCEVERIFY = 178
)

func fundingTxScript(senderPubKey, receiverPubKey *btcutil.AddressPubKey, timeout int64) ([]byte, error) {
	b := txscript.NewScriptBuilder()
	b.AddOp(txscript.OP_IF)
	b.AddInt64(2)
	b.AddData(senderPubKey.ScriptAddress())
	b.AddData(receiverPubKey.ScriptAddress())
	b.AddInt64(2)
	b.AddOp(txscript.OP_CHECKMULTISIG)
	b.AddOp(txscript.OP_ELSE)
	b.AddInt64(timeout)
	b.AddOp(OP_CHECKSEQUENCEVERIFY)
	b.AddOp(txscript.OP_DROP)
	b.AddOp(txscript.OP_DUP)
	b.AddOp(txscript.OP_HASH160)
	b.AddData(senderPubKey.AddressPubKeyHash().ScriptAddress())
	b.AddOp(txscript.OP_EQUALVERIFY)
	b.AddOp(txscript.OP_CHECKSIG)
	b.AddOp(txscript.OP_ENDIF)
	return b.Script()
}

func (s *SharedState) GetFundingScript() ([]byte, string, error) {
	script, err := fundingTxScript(s.SenderPubKey, s.ReceiverPubKey, s.Timeout)
	if err != nil {
		return nil, "", err
	}

	scriptHash, err := btcutil.NewAddressScriptHash(script, s.Net)
	if err != nil {
		return nil, "", err
	}

	return script, scriptHash.String(), nil
}

func (s *SharedState) spendFundingTx() (*wire.MsgTx, error) {
	txid, err := chainhash.NewHashFromStr(s.FundingTxID)
	if err != nil {
		return nil, err
	}
	txin := wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  *txid,
			Index: s.FundingVout,
		},
	}

	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&txin)
	return tx, nil
}

func sendToAddress(amount int64, addr *btcutil.AddressPubKey) (*wire.TxOut, error) {
	pkscript, err := txscript.PayToAddrScript(addr.AddressPubKeyHash())
	if err != nil {
		return nil, err
	}
	return &wire.TxOut{
		Value:    amount,
		PkScript: pkscript,
	}, nil
}

func (s *SharedState) GetClosureTx() (*wire.MsgTx, error) {
	receiveAmount := s.Balance
	senderAmount := s.FundingAmount - s.Balance - s.Fee

	tx, err := s.spendFundingTx()
	if err != nil {
		return nil, err
	}

	if receiveAmount > 0 {
		txout, err := sendToAddress(receiveAmount, s.ReceiverPubKey)
		if err != nil {
			return nil, err
		}
		tx.AddTxOut(txout)
	}

	if senderAmount > 0 {
		txout, err := sendToAddress(senderAmount, s.SenderPubKey)
		if err != nil {
			return nil, err
		}
		tx.AddTxOut(txout)
	}

	return tx, nil
}

func (s *SharedState) GetClosureTxSigned(senderSig, receiverSig []byte) ([]byte, error) {
	tx, err := s.GetClosureTx()
	if err != nil {
		return nil, err
	}

	script, _, err := s.GetFundingScript()
	if err != nil {
		return nil, err
	}

	b := txscript.NewScriptBuilder()
	b.AddOp(txscript.OP_FALSE)
	b.AddData(senderSig)
	b.AddData(receiverSig)
	b.AddOp(txscript.OP_TRUE)
	b.AddData(script)
	finalScript, err := b.Script()
	if err != nil {
		return nil, err
	}

	tx.TxIn[0].SignatureScript = finalScript

	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (s *SharedState) GetRefundTxSigned(privKey *btcec.PrivateKey) ([]byte, error) {
	tx, err := s.spendFundingTx()
	if err != nil {
		return nil, err
	}

	amount := s.FundingAmount - s.Fee
	txout, err := sendToAddress(amount, s.SenderPubKey)
	if err != nil {
		return nil, err
	}
	tx.AddTxOut(txout)

	tx.TxIn[0].Sequence = uint32(s.Timeout)

	script, _, err := s.GetFundingScript()
	if err != nil {
		return nil, err
	}

	sig, err := txscript.RawTxInSignature(
		tx, 0, script, txscript.SigHashAll, privKey)
	if err != nil {
		return nil, err
	}

	b := txscript.NewScriptBuilder()
	b.AddData(sig)
	b.AddData(s.SenderPubKey.ScriptAddress())
	b.AddOp(txscript.OP_FALSE)
	b.AddData(script)
	finalScript, err := b.Script()
	if err != nil {
		return nil, err
	}

	tx.TxIn[0].SignatureScript = finalScript

	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (s *SharedState) validateTx(rawTx []byte) error {
	script, err := fundingTxScript(s.SenderPubKey, s.ReceiverPubKey, s.Timeout)
	if err != nil {
		return err
	}
	addr, err := btcutil.NewAddressScriptHash(script, s.Net)
	if err != nil {
		return err
	}
	pkscript, err := txscript.PayToAddrScript(addr)
	if err != nil {
		return err
	}

	var tx wire.MsgTx
	if err := tx.BtcDecode(bytes.NewReader(rawTx), 2); err != nil {
		return err
	}

	if len(tx.TxIn) != 1 {
		return errors.New("wrong number of inputs")
	}

	engine, err := txscript.NewEngine(pkscript, &tx, 0, 0, nil)
	if err != nil {
		return err
	}

	return engine.Execute()
}
