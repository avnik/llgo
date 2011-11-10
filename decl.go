/*
Copyright (c) 2011 Andrew Wilkins <axwalk@gmail.com>

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the "Software"), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies
of the Software, and to permit persons to whom the Software is furnished to do
so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
*/

package main

import (
    "fmt"
    "go/ast"
    "go/token"
    "go/types"
    "reflect"
    "github.com/axw/gollvm/llvm"
)

func (self *Visitor) VisitFuncProtoDecl(f *ast.FuncDecl) llvm.Value {
    fn_type := self.VisitFuncType(f.Type)
    fn_name := f.Name.String()
    var fn llvm.Value
    if self.modulename == "main" && fn_name == "main" {
        fn = llvm.AddFunction(self.module, "main", fn_type)
        fn.SetLinkage(llvm.ExternalLinkage)
    } else {
        if fn_name == "init" {fn_name = ""} // Make init functions anonymous
        fn = llvm.AddFunction(self.module, fn_name, fn_type)
        fn.SetFunctionCallConv(llvm.FastCallConv) // XXX
    }
    return fn
}

func (self *Visitor) VisitFuncDecl(f *ast.FuncDecl) llvm.Value {
    name := f.Name.String()
    obj := f.Name.Obj

    var fn llvm.Value
    if obj.Data != nil {
        var ok bool
        fn, ok = (obj.Data).(llvm.Value)
        if !ok {panic("obj.Data is not nil and is not a llvm.Value")}
    } else {
        fn = self.VisitFuncProtoDecl(f)
    }

    entry := llvm.AddBasicBlock(fn, "entry")
    self.builder.SetInsertPointAtEnd(entry)

    self.functions = append(self.functions, fn)
    if f.Body != nil {self.VisitBlockStmt(f.Body)}
    self.functions = self.functions[0:len(self.functions)-1]
    fn_type := fn.Type().ReturnType() // fn.Type() is a pointer-to-function

    if fn_type.ReturnType().TypeKind() == llvm.VoidTypeKind {
        last_block := fn.LastBasicBlock()
        lasti := last_block.LastInstruction()
        if lasti.IsNil() || lasti.Opcode() != llvm.Ret {
            // Assume nil return type, AST should be checked first.
            self.builder.CreateRetVoid()
        }
    }

    // Is it an 'init' function? Then record it.
    if name == "init" {
        // TODO
        //self.initfunctions = append(self.initfunctions, fn)
        panic("TODO")
    } else {
        obj.Data = fn
    }
    return fn
}

func (self *Visitor) VisitValueSpec(valspec *ast.ValueSpec, isconst bool) {
    // TODO (from language spec)
    // If the type is absent and the corresponding expression evaluates to
    // an untyped constant, the type of the declared variable is bool, int,
    // float64, or string respectively, depending on whether the value is
    // a boolean, integer, floating-point, or string constant.

    // TODO value type

    var iota_obj *ast.Object = types.Universe.Lookup("iota")
    defer func(data interface{}) {
        iota_obj.Data = data
    }(iota_obj.Data)

    for i, name_ := range valspec.Names {
        // We may resolve constants in the process of resolving others.
        obj := name_.Obj
        if _, isvalue := (obj.Data).(llvm.Value); isvalue {continue}

        // Set iota if necessary.
        if isconst {
            if iota_, isint := (name_.Obj.Data).(int); isint {
                iota_value := llvm.ConstInt(
                    llvm.Int32Type(), uint64(iota_), false)
                iota_obj.Data = iota_value

                // Con objects with an iota have an embedded ValueSpec
                // in the Decl field. We'll just pull it out and use it
                // for evaluating the expression below.
                valspec, _ = (name_.Obj.Decl).(*ast.ValueSpec)
            }
        }

        // Expression may have side-effects, so compute it regardless of
        // whether it'll be assigned to a name.
        var value llvm.Value
        if valspec.Values[i] != nil {
            value = self.VisitExpr(valspec.Values[i])
        }

        name := name_.String()
        if name != "_" {
            exported := name_.IsExported()
            constprim := !(value.IsAConstantInt().IsNil() ||
                           value.IsAConstantFP().IsNil())
            if isconst && constprim && !exported {
                // Not exported, and it's a constant. Let's forego creating the
                // internal constant and just pass around the llvm.Value.
                obj.Kind = ast.Con // Change to constant
                obj.Data = value
            } else {
                init_ := value

                // If we're assigning another constant to the constant, then
                // just take its initializer.
                if isconst && !init_.IsNil() && isglobal(init_) {
                    init_ = init_.Initializer()
                }

                value = llvm.AddGlobal(self.module, init_.Type(), name)
                if !init_.IsNil() {value.SetInitializer(init_)}
                if isconst {value.SetGlobalConstant(true)}
                if !exported {value.SetLinkage(llvm.InternalLinkage)}
                obj.Data = value

                // If it's not an array, we should mark the value as being
                // "indirect" (i.e. it must be loaded before use).
                if init_.Type().TypeKind() != llvm.ArrayTypeKind {
                    setindirect(value)
                }
            }
        }
    }
}

func (self *Visitor) VisitGenDecl(decl *ast.GenDecl) {
    switch decl.Tok {
    case token.IMPORT: // No-op (handled in VisitFile
    case token.TYPE: {
        panic("Unhandled type declaration");
    }
    case token.CONST: {
        for _, spec := range decl.Specs {
            valspec, _ := spec.(*ast.ValueSpec)
            self.VisitValueSpec(valspec, true)
        }
    }
    case token.VAR: {
        for _, spec := range decl.Specs {
            valspec, _ := spec.(*ast.ValueSpec)
            self.VisitValueSpec(valspec, false)
        }
    }
    }
}

func (self *Visitor) VisitDecl(decl ast.Decl) llvm.Value {
    switch x := decl.(type) {
    case *ast.FuncDecl: return self.VisitFuncDecl(x)
    case *ast.GenDecl: {
        self.VisitGenDecl(x)
        return llvm.Value{nil}
    }
    }
    panic(fmt.Sprintf("Unhandled decl (%s) at %s\n",
                      reflect.TypeOf(decl),
                      self.fileset.Position(decl.Pos())))
}

// vim: set ft=go :

