package gorgonia

/*
This file holds all the Ops that are related to doing math-related work. Due to the numerousness of
mathematical operations, they're classified into 3 main types:
	elemBinOp - a representation of a binary mathematical operation that is performed elementwise (example: +, *, -, or >, <)
	elemUnaryOp - a representation of a mathematical operation that is performed elmentwise
	linAlgBinOp - a representation of a binary mathematical operation that is performed on matrices

The individual operators are further exanded on operator*.go files. Their datatypes are often embedded in the datatypes here.

For all data type, the methods are standardized by arrangement in the order the Op interface is defined.
Any additional interfaces that the data type fulfils will be declared AFTER the Op interface methods.
*/

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"

	"github.com/chewxy/gorgonia/tensor"
	tf32 "github.com/chewxy/gorgonia/tensor/f32"
	tf64 "github.com/chewxy/gorgonia/tensor/f64"
	"github.com/chewxy/gorgonia/tensor/types"
	"github.com/pkg/errors"
)

/* ELEMENTWISE BINARY OPERATION */

// elemBinOp is the representation of an operation that is to be performed elementwise
type elemBinOp struct {
	ʘBinaryOperator
	arg0, arg1 Type // pruned types only plz
	retSame    bool // for comparison ops, return same type?
}

func newEBOByType(ot ʘBinaryOperatorType, at, bt Type) elemBinOp {
	var binOp ʘBinaryOperator
	switch att := at.(type) {
	case Dtype:
		switch bt.(type) {
		case Dtype:
			binOp = scalarBinOp{
				ʘBinaryOperatorType: ot,
				t:                   att,
			}
		case *TensorType:
			binOp = tBinOp{
				ʘBinaryOperatorType: ot,
				tensorLeft:          false,
			}
		default:
			panic(fmt.Sprintf("Unsupported type of b %v!", bt))
		}
	case *TensorType:
		binOp = tBinOp{
			ʘBinaryOperatorType: ot,
			tensorLeft:          true,
		}
	default:
		panic(fmt.Sprintf("Unsupported type of a %v!", at))
	}
	return elemBinOp{
		ʘBinaryOperator: binOp,
		arg0:            at,
		arg1:            bt,
	}
}

func newElemBinOp(ot ʘBinaryOperatorType, a, b *Node) elemBinOp {
	at := prune(a.t)
	bt := prune(b.t)

	return newEBOByType(ot, at, bt)
}

func (op elemBinOp) Arity() int { return 2 }

// elemBinOp has either of these types:
// 		elemBinOp :: (Floats a) ⇒ Tensor a → Tensor a → Tensor a
// 		elemBinOp :: (Floats a) ⇒ Tensor a → a → Tensor a
//		elemBinOp :: (Floats a) ⇒ a → Tensor a → a
//		elemBinOp :: (Floats a) ⇒ a → a → a
//		elemBinOp :: (Floats a) ⇒ a → a → Bool
// 		elemBinOp :: (Floats a) ⇒ Tensor a → Tensor a → Tensor Bool
// 		elemBinOp :: (Floats a) ⇒ Tensor a → a → Tensor Bool
//		elemBinOp :: (Floats a) ⇒ a → Tensor a → Bool
//
// To make things clearer, it helps to consider elemBinOp to be the representation of
// a dispatch table for different functions. In a sense it's "overloading" functions.
//
// At the moment, due to my refusal to create a sum type (which requires more finnicking with data constructors)
// Type() happens pretty much at close to run time
func (op elemBinOp) Type() Type {
	a := newTypeVariable("a", withTVConstraints(floats))

	var a0, a1, retType Type
	switch arg0 := op.arg0.(type) {
	case *TensorType:
		a0 = fromTensorType(arg0, a)
		retType = fromTensorType(arg0, a)
	case *typeVariable:
		if instance, ok := arg0.instance.(*TensorType); ok {
			a0 = fromTensorType(instance, a)
			retType = fromTensorType(instance, a)
		} else {
			a0 = a
			retType = a
		}
	default:
		a0 = a
		retType = a
	}

	switch arg1 := op.arg1.(type) {
	case *TensorType:
		a1 = fromTensorType(arg1, a)
		retType = fromTensorType(arg1, a)
	case *typeVariable:
		if instance, ok := arg1.instance.(*TensorType); ok {
			a1 = fromTensorType(instance, a)
			retType = fromTensorType(instance, a)
		} else {
			a1 = a
		}
	default:
		a1 = a
	}

	if op.isArith() || (!op.isArith() && op.retSame) {
		return newFunctionType(a0, a1, retType)
	}

	switch rt := retType.(type) {
	case *TensorType:
		rt.of = Bool
	default:
		retType = Bool
	}

	return newFunctionType(a0, a1, retType)
}

// elemBinOp has these allowed shapes:
// 		op :: () → () → ()
//		op :: () → (...) → (...)
//		op :: (...) → () → (...)
func (op elemBinOp) InferShape(inputs ...DimSizer) (retVal types.Shape, err error) {
	shapeLogf("Inferring shape of %v", op)
	enterLoggingContext()
	defer leaveLoggingContext()

	if inputs[0] == nil || inputs[1] == nil {
		return nil, errors.Errorf(nyiFail, "elemBinOp.inferShape", "runtime impl")
	}

	x, y := inputs[0].(types.Shape), inputs[1].(types.Shape) // passing any other types of DimSizer will cause panics. Which is a Good Thing
	switch {
	case x.IsScalar() && y.IsScalar():
		retVal = scalarShape
	case x.IsScalar() && !y.IsScalar():
		retVal = y
	case !x.IsScalar() && y.IsScalar():
		retVal = x
	case !x.IsScalar() && !y.IsScalar():
		if !x.Eq(y) {
			// error
		}
		retVal = x
	}
	return
}

// diffWRT gives info on whether or not the operation is actually differentiable
// For example, this is differentiable:
//		c = a ** b
// The result of the differentiation wrt to a and b would be:
// 		dc/da = b * a ** (b-1)
// 		dc/db = <insert exp rule expansion here.. don't quite remember it> //TODO
//
// However, operators like < and > are NOT differentiable
//
// This method returns a slice of bools, indicating whether differentiation with regards to its operands
// can be done. Since binOp has 2 operands, we'll return a slice
func (op elemBinOp) DiffWRT(inputs int) []bool {
	if inputs != 2 {
		panic(fmt.Sprintf(binOpFail, inputs))
	}

	b := op.ʘBinaryOperator.binOpType()

	if b >= maxʘBinaryOpType {
		panic("Unsupported unary operator is not differentiable")
	}

	if b.isArith() {
		return []bool{true, true}
	}
	return []bool{false, false}
}

func (op elemBinOp) SymDiff(inputs Nodes, output, gradNode *Node) (retVal Nodes, err error) {
	if err = checkArity(op, len(inputs)); err != nil {
		return
	}

	b := op.ʘBinaryOperator.binOpType()

	if retVal, err = ʘBinOpDiffExprs[b](inputs[0], inputs[1], output, gradNode); err == nil {
		for _, n := range retVal {
			n.setGroup(gradClust)
		}
	}

	// needed to handle scalar gradients such as b in the logit regression example
	for i, grad := range retVal {
		if inputs[i].IsScalar() && !grad.IsScalar() {
			if retVal[i], err = Sum(grad); err != nil {
				err = errors.Wrap(err, operationError)
				return
			}
		}
	}

	return
}

func (op elemBinOp) Do(values ...Value) (Value, error) {
	return op.ʘBinaryOperator.Do(op.retSame, values...)
}

func (op elemBinOp) DoDiff(inputs Nodes, output *Node) (err error) {
	if err = checkArity(op, len(inputs)); err != nil {
		return
	}

	b := op.ʘBinaryOperator.binOpType()
	if err = ʘBinOpDiffFns[b](inputs[0], inputs[1], output); err != nil {
		return errors.Wrapf(err, autodiffFail, b)
	}

	//handle scalar gradients
	for _, in := range inputs {
		indv := in.boundTo.(*dualValue)
		if _, ok := indv.d.(Scalar); in.IsScalar() && !ok {
			indvdT := indv.d.(Tensor)
			var d Value
			var t types.Tensor
			if t, err = tensor.Sum(indvdT.Tensor); err != nil {
				return errors.Wrap(err, operationError)
			}

			if d, err = anyToValue(t.ScalarValue()); err != nil {
				return errors.Wrap(err, operationError)
			}
			returnTensor(indvdT)
			indv.SetDeriv(d)
		}
	}
	return
}

func (op elemBinOp) ReturnsPtr() bool {
	if _, ok := op.arg0.(*TensorType); ok {
		return true
	} else if _, ok := op.arg1.(*TensorType); ok {
		return true
	}

	return false
}

func (op elemBinOp) CallsExtern() bool { return false } // for now
func (op elemBinOp) OverwritesInput() int {
	if _, ok := op.arg0.(*TensorType); ok {
		return 0
	}

	if _, ok := op.arg1.(*TensorType); ok {
		return 1
	}
	return -1
}

func (op elemBinOp) WriteHash(h hash.Hash) {
	if err := binary.Write(h, binary.LittleEndian, op.binOpType()); err != nil {
		panic(err)
	}

	fmt.Fprintf(h, "%v,%v", op.arg0, op.arg1)
}

func (op elemBinOp) Hashcode() uint32 {
	h := fnv.New32a()
	op.WriteHash(h)
	return h.Sum32()
}

// Fulfils UsePreallocDoer interface
func (op elemBinOp) UsePreallocDo(prealloc Value, inputs ...Value) (retVal Value, err error) {
	if !op.ReturnsPtr() {
		return op.Do(inputs...)
	}

	if pd, ok := op.ʘBinaryOperator.(UsePreallocDoer); ok {
		return pd.UsePreallocDo(prealloc, inputs...)
	}

	return op.Do(inputs...)
}

// Fulfils UnsafeDoer interface
func (op elemBinOp) UnsafeDo(inputs ...Value) (retVal Value, err error) {
	if !op.ReturnsPtr() {
		return op.Do(inputs...)
	}

	if ud, ok := op.ʘBinaryOperator.(UnsafeDoer); ok {
		return ud.UnsafeDo(inputs...)
	}

	return op.Do(inputs...)
}

// Fulfils the IncrDoer interface
func (op elemBinOp) IncrDo(incr Value, inputs ...Value) (err error) {
	if !op.ReturnsPtr() {
		var retVal Value
		if retVal, err = op.Do(inputs...); err != nil {
			return errors.Wrapf(err, doFail, op)
		}

		add := newEBOByType(addOpType, incr.Type(), retVal.Type())
		if retVal, err = add.UnsafeDo(incr, retVal); err != nil {
			return errors.Wrapf(err, unsafeDoFail, add)
		}
		err = noIncrErr{retVal}
		return
	}

	if id, ok := op.ʘBinaryOperator.(IncrDoer); ok {
		return id.IncrDo(incr, inputs...)
	}
	panic("unreachable")
}

// Fulfils the BinaryOp interface
func (op elemBinOp) IsBinary() bool { return true }

/* ELEMENTWISE UNARY OP */

type elemUnaryOp struct {
	ʘUnaryOperator

	argTensor     bool
	numericResult bool // indicate if boolean results should be converted to 1 and 0 in the respective Dtype
}

func newElemUnaryOp(op ʘUnaryOperatorType, a *Node) elemUnaryOp {
	dt, err := dtypeOf(a.t)
	if err != nil {
		panic(err)
	}

	_, isTensor := a.t.(*TensorType)

	var operator ʘUnaryOperator
	switch dt {
	case Float32:
		operator = sf32UnaryOperators[op]
	case Float64:
		operator = sf64UnaryOperators[op]
	}

	return elemUnaryOp{
		ʘUnaryOperator: operator,
		argTensor:      isTensor,
	}
}

func (op elemUnaryOp) Arity() int { return 1 }

// all pointwise unary operations have this type:
//		op :: (Arithable a) ⇒ a → a
func (op elemUnaryOp) Type() Type {
	a := newTypeVariable("a", withTVConstraints(arithable))
	return newFunctionType(a, a)
}

func (op elemUnaryOp) InferShape(inputs ...DimSizer) (retVal types.Shape, err error) {
	if inputs[0] == nil {
		return nil, errors.Errorf(nyiFail, "inferShape", "nil shape")
	}

	return inputs[0].(types.Shape), nil
}

// diffWRT gives info on whether or not the operation is actually differentiable wrt to its inputs
//
// some operations, such as ceil(), sign(), floor cannot be differentiated wrt to its inputs (or I don't actually know how to do them)
func (op elemUnaryOp) DiffWRT(inputs int) []bool {
	if inputs != 1 {
		panic(fmt.Sprintf("unary operator only supports one input, got %d instead", inputs))
	}

	u := op.ʘUnaryOperator.unaryOpType()

	if u >= maxʘUnaryOperator {
		panic("Unsupported unary operator is not differentiable")
	}
	return []bool{ʘUnaryOpDifferentiable[u]}
}

func (op elemUnaryOp) SymDiff(inputs Nodes, output, gradNode *Node) (retVal Nodes, err error) {
	if err = checkArity(op, len(inputs)); err != nil {
		return
	}

	u := op.ʘUnaryOperator.unaryOpType()

	var n *Node
	if n, err = ʘUnaryOpDiffExprs[u](inputs[0], output, gradNode); err == nil {
		n.setGroup(gradClust)
		retVal = Nodes{n}
	}
	return
}

func (op elemUnaryOp) DoDiff(inputs Nodes, output *Node) (err error) {
	if err = checkArity(op, len(inputs)); err != nil {
		return
	}

	u := op.ʘUnaryOperator.unaryOpType()
	return ʘUnaryOpDiffFns[u](inputs[0], output)
}

func (op elemUnaryOp) Do(inputs ...Value) (retVal Value, err error) { return op.do(inputs) }

func (op elemUnaryOp) ReturnsPtr() bool {
	if op.argTensor {
		return true
	}
	return false
}

func (op elemUnaryOp) OverwritesInput() int {
	if op.argTensor {
		return 0
	}
	return -1
}

func (op elemUnaryOp) CallsExtern() bool { return false }

func (op elemUnaryOp) WriteHash(h hash.Hash) {
	if err := binary.Write(h, binary.LittleEndian, op.unaryOpType()); err != nil {
		panic(err)
	}

	if op.argTensor {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
}

func (op elemUnaryOp) Hashcode() uint32 {
	h := fnv.New32a()
	op.WriteHash(h)
	return h.Sum32()
}

// fulfils UnsafeDoer interface
func (op elemUnaryOp) UnsafeDo(inputs ...Value) (Value, error) {
	return op.do(inputs, types.UseUnsafe())
}

// fulfils UnaryOp interface

func (op elemUnaryOp) isUnary() bool { return true }

// misc private methods

func (op elemUnaryOp) do(inputs []Value, opts ...types.FuncOpt) (retVal Value, err error) {
	if err = checkArity(op, len(inputs)); err != nil {
		return
	}

	a := inputs[0]
	switch v := a.(type) {
	case Tensor:
		switch vt := v.Tensor.(type) {
		case *tf64.Tensor:
			opFn := op.ʘUnaryOperator.(*sf64UnaryOperator)
			fn := (func(float64) float64)(*opFn)

			// TODO: this is pretty shit.... the tf64 lib provides a whole bunch of these
			var t types.Tensor
			if t, err = vt.Apply(fn, opts...); err != nil {
				return nil, errors.Wrap(err, applyFail)
			}
			retVal = FromTensor(t)
		case *tf32.Tensor:
			opFn := op.ʘUnaryOperator.(*sf32UnaryOperator)
			fn := (func(float32) float32)(*opFn)

			// TODO: this is pretty shit.... the tf64 lib provides a whole bunch of these
			var t types.Tensor
			if t, err = vt.Apply(fn, opts...); err != nil {
				return nil, errors.Wrap(err, applyFail)
			}
			retVal = FromTensor(t)
		default:
			return nil, errors.Errorf(nyiFail, "elemUnaryOp.do", v.Tensor)
		}
	case Scalar:
		switch v.t {
		case Float32:
			f := v.v.(float32)
			opFn := op.ʘUnaryOperator.(*sf32UnaryOperator)
			retVal = NewScalarValue((*opFn)(f))
		case Float64:
			f := v.v.(float64)
			opFn := op.ʘUnaryOperator.(*sf64UnaryOperator)
			retVal = NewScalarValue((*opFn)(f))
		default:
			return nil, errors.Errorf(nyiFail, "elemUnaryOp.do", v.t)
		}
	}
	return
}

/* LINEAR ALGEBRA RELATED OPERATIONS */

type linAlgBinOp struct {
	āBinaryOperator
	transA, transB bool
}

func (op linAlgBinOp) Arity() int { return 2 }

func (op linAlgBinOp) InferShape(inputs ...DimSizer) (retVal types.Shape, err error) {
	shapeLogf("Inferring shape of %v", op)
	enterLoggingContext()
	defer leaveLoggingContext()

	if inputs[0] == nil || inputs[1] == nil {
		return nil, nyi("InferShape for linalgBinOp", "runtime impl")
	}

	x, y := inputs[0].(types.Shape), inputs[1].(types.Shape)
	if x == nil || y == nil {
		return nil, errors.Errorf("Cannot infer shape from %v %v", x, y)
	}

	shapeLogf("x.shape: %v; y.shape: %v", x, y)
	// TODO: add checks for tensors greater than 2 d

	switch op.āBinaryOperator {
	case matMulOperator:
		if op.transA {
			x = transpose(x)
		}
		if op.transB {
			y = transpose(y)
		}

		retVal = types.Shape{x[0], y[1]}
	case matVecMulOperator:
		if op.transA {
			x = transpose(x)
		}

		if x[0] != y[0] && x[1] != y[0] {
			return nil, errors.Errorf("Incompatible shapes: %v and %v", x, y)
		}

		retVal = types.Shape{x[0], 1}
	case vecDotOperator:
		retVal = scalarShape
	case outerProdOperator:
		// outerprods only handles vec x vec for now
		retVal = types.Shape{x.TotalSize(), y.TotalSize()}
	}
	return
}

func (op linAlgBinOp) SymDiff(inputs Nodes, output, gradNode *Node) (retVal Nodes, err error) {
	if err = checkArity(op, len(inputs)); err != nil {
		return
	}

	o := op.āBinaryOperator

	if retVal, err = āBinOpDiffExprs[o](op.transA, op.transB, inputs[0], inputs[1], output, gradNode); err != nil {
		return nil, errors.Wrap(err, "Failed to differentiate expressions")
	}

	for _, n := range retVal {
		n.setGroup(gradClust)
	}
	return
}

func (op linAlgBinOp) DoDiff(inputs Nodes, output *Node) (err error) {
	if err = checkArity(op, len(inputs)); err != nil {
		return
	}

	o := op.āBinaryOperator
	return āBinOpDiffs[o](op.transA, op.transB, inputs[0], inputs[1], output)
}

func (op linAlgBinOp) Do(inputs ...Value) (retVal Value, err error) { return op.do(inputs) }
func (op linAlgBinOp) ReturnsPtr() bool                             { return true }
func (op linAlgBinOp) OverwritesInput() int                         { return -1 }
func (op linAlgBinOp) CallsExtern() bool {
	if op.āBinaryOperator != vecDotOperator {
		return true
	}
	return false
}

func (op linAlgBinOp) WriteHash(h hash.Hash) {
	if err := binary.Write(h, binary.LittleEndian, op.āBinaryOperator); err != nil {
		panic(err)
	}

	if op.transA {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}

	if op.transB {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
}

func (op linAlgBinOp) Hashcode() uint32 {
	h := fnv.New32a()
	op.WriteHash(h)
	return h.Sum32()
}

func (op linAlgBinOp) String() string {
	var buf bytes.Buffer

	switch op.āBinaryOperator {
	case matMulOperator, matVecMulOperator:
		buf.WriteString("A")
	case vecDotOperator, outerProdOperator:
		buf.WriteString("a")
	}

	if op.transA {
		buf.WriteString("ᵀ")
	}

	switch op.āBinaryOperator {
	case matMulOperator:
		fmt.Fprintf(&buf, " %v B", op.āBinaryOperator)
	case matVecMulOperator, vecDotOperator, outerProdOperator:
		fmt.Fprintf(&buf, " %v b", op.āBinaryOperator)
	}

	if op.transB {
		buf.WriteString("ᵀ")
	}

	return buf.String()
}

// fulfils IncrDoer
func (op linAlgBinOp) IncrDo(incr Value, inputs ...Value) (err error) {
	t, ok := incr.(Tensor)
	var reuse types.Tensor

	if ok {
		reuse = t.Tensor
		_, err = op.do(inputs, types.WithIncr(reuse))
		return
	}

	var retVal Value
	if retVal, err = op.do(inputs); err != nil {
		return errors.Wrapf(err, doFail, op)
	}

	add := newEBOByType(addOpType, incr.Type(), retVal.Type())
	if retVal, err = add.UnsafeDo(incr, retVal); err != nil {
		return errors.Wrapf(err, unsafeDoFail, add)
	}

	err = noIncrErr{retVal}
	return
}

// fulfils UsePreallocDoer
func (op linAlgBinOp) UsePreallocDo(prealloc Value, inputs ...Value) (retVal Value, err error) {
	t, ok := prealloc.(Tensor)
	if !ok {
		return nil, errors.Errorf("Expected Tensor as preallocated value. Got %v of %T instead", prealloc, prealloc)
	}

	return op.do(inputs, types.WithReuse(t.Tensor))
}

// fulfils BinaryOp
func (op linAlgBinOp) IsBinary() bool { return true }

/* PRIVATE METHODS */

func (op linAlgBinOp) do(inputs []Value, opts ...types.FuncOpt) (retVal Value, err error) {
	if err = checkArity(op, len(inputs)); err != nil {
		return
	}

	a, b := inputs[0].(Tensor), inputs[1].(Tensor)

	if op.transA {
		if err = a.Tensor.T(); err != nil {
			return nil, errors.Wrap(err, tFail)
		}
		// untranspose
		defer a.Tensor.T()
	}

	if op.transB {
		if err = b.Tensor.T(); err != nil {
			return nil, errors.Wrap(err, tFail)
		}
		// untranspose
		defer b.Tensor.T()
	}

	var r interface{}
	switch op.āBinaryOperator {
	case matMulOperator:
		r, err = tensor.MatMul(a.Tensor, b.Tensor, opts...)
	case matVecMulOperator:
		r, err = tensor.MatVecMul(a.Tensor, b.Tensor, opts...)
	case vecDotOperator:
		var ret types.Tensor
		ret, err = tensor.Inner(a.Tensor, b.Tensor)
		r = ret.ScalarValue()
	case outerProdOperator:
		r, err = tensor.Outer(a.Tensor, b.Tensor, opts...)
	}
	if err == nil {
		retVal, err = anyToValue(r)
	} else {
		return nil, errors.Wrap(err, "Failed to carry out operation")
	}
	return
}
