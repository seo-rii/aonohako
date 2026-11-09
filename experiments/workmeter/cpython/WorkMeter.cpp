#include "llvm/IR/PassManager.h"
#include "llvm/IR/IRBuilder.h"
#include "llvm/IR/Module.h"
#include "llvm/IR/Intrinsics.h"
#include "llvm/IR/Instructions.h"
#include "llvm/Passes/PassBuilder.h"
#include "llvm/Passes/PassPlugin.h"
#include "llvm/Transforms/Utils/ModuleUtils.h"
#include "llvm/Transforms/Utils/BasicBlockUtils.h"
#include "llvm/Support/MD5.h"
#include "llvm/Support/ModRef.h"
#include "llvm/Support/FileSystem.h"
#include "llvm/Support/raw_ostream.h"
#include "llvm/Support/ErrorHandling.h"
#include <cstdlib>
#include <string>
#include <vector>
using namespace llvm;
static std::string quote(StringRef s) { std::string out="\""; for(unsigned char c:s) { if(c=='\\'||c=='\"'){out+='\\';out+=c;} else if(c<32){ const char *h="0123456789abcdef";out+="\\u00";out+=h[c>>4];out+=h[c&15]; }else out+=c;}return out+'\"'; }
struct WorkMeter : PassInfoMixin<WorkMeter> {
 PreservedAnalyses run(Module &M, ModuleAnalysisManager &) {
  struct Block { BasicBlock *bb; uint64_t weight; std::string function; unsigned index; };
  std::vector<Block> blocks;
  for(auto &F:M) {
   if(!F.isIntrinsic()&&!F.getName().starts_with("__wm_")){
    F.setMemoryEffects(MemoryEffects::unknown()); F.removeFnAttr(Attribute::NoSync); F.removeFnAttr(Attribute::Speculatable);
   }
   if(F.isDeclaration() || F.getName().starts_with("__wm_"))continue; unsigned index=0;
   if(F.hasFnAttribute(Attribute::Naked))report_fatal_error("workmeter: naked function unsupported");
   for(auto &B:F) { uint64_t w=0; for(auto &I:B) { if(isa<PHINode>(I))continue; if(auto *II=dyn_cast<IntrinsicInst>(&I)) { if(II->isDebugOrPseudoInst() || II->isLifetimeStartOrEnd())continue; } ++w; }
    if(B.getFirstInsertionPt()==B.end())report_fatal_error("workmeter: uninstrumentable nonempty block");
    blocks.push_back({&B,w,F.getName().str(),index++});
   }
  }
  if(blocks.empty())return PreservedAnalyses::all();
  // Call-site effects are independent of the callee definition. Instrumented
  // callees write escaped counters, including across TUs and indirect calls.
  for(auto &F:M)for(auto &B:F)for(auto &I:B)
   if(auto *call=dyn_cast<CallBase>(&I))if(!isa<IntrinsicInst>(call)) {
    call->setMemoryEffects(MemoryEffects::unknown());
    call->removeFnAttr(Attribute::NoSync);
    call->removeFnAttr(Attribute::Speculatable);
   }
  std::string meta="{\"schema\":1,\"metric\":\"llvm_ir_block_work\",\"source\":"+quote(M.getSourceFileName())+",\"weight_policy\":\"one_per_optimized_IR_instruction_excluding_PHI_debug_lifetime; calls_and_memory_intrinsics_one\",\"blocks\":[";
  for(size_t i=0;i<blocks.size();++i){if(i)meta+=",";auto &b=blocks[i];meta+="["+std::to_string(i)+","+std::to_string(b.weight)+","+quote(b.function)+","+std::to_string(b.index)+"]";}meta+="]}";
  MD5 hash; hash.update(meta); MD5::MD5Result digest;hash.final(digest);SmallString<32> id;MD5::stringifyResult(digest,id);
  const char *dir=std::getenv("WM_METADATA_DIR");if(!dir)report_fatal_error("WM_METADATA_DIR required");
  std::string path=std::string(dir)+"/"+id.str().str()+".json";std::error_code ec;raw_fd_ostream out(path,ec,sys::fs::OF_Text);if(ec)report_fatal_error("workmeter metadata write failed");out<<meta<<"\n";out.close();
  auto &C=M.getContext();auto *u64=Type::getInt64Ty(C);auto *arr=ArrayType::get(u64,blocks.size());
  auto *counts=new GlobalVariable(M,arr,false,GlobalValue::InternalLinkage,ConstantAggregateZero::get(arr),"__wm_counts");counts->setAlignment(Align(64));
  std::vector<Constant*> ws;for(auto &b:blocks)ws.push_back(ConstantInt::get(u64,b.weight));
  auto *weights=new GlobalVariable(M,arr,true,GlobalValue::InternalLinkage,ConstantArray::get(arr,ws),"__wm_weights");
  auto *ptr=PointerType::getUnqual(C);auto reg=M.getOrInsertFunction("__wm_register",Type::getVoidTy(C),ptr,ptr,ptr,u64);
  auto ov=M.getOrInsertFunction("__wm_overflow",Type::getVoidTy(C));
  auto *init=Function::Create(FunctionType::get(Type::getVoidTy(C),false),GlobalValue::InternalLinkage,"__wm_init",M);
  auto *entry=BasicBlock::Create(C,"entry",init);IRBuilder<> ib(entry);
  ib.CreateCall(reg,{ib.CreateGlobalStringPtr(id.str()),counts,weights,ConstantInt::get(u64,blocks.size())});ib.CreateRetVoid();appendToGlobalCtors(M,init,65535);
  for(size_t i=0;i<blocks.size();++i){IRBuilder<> b(&*blocks[i].bb->getFirstInsertionPt());
   auto *slot=b.CreateConstInBoundsGEP2_64(arr,counts,0,i);auto *old=b.CreateLoad(u64,slot);auto *next=b.CreateAdd(old,ConstantInt::get(u64,1));b.CreateStore(next,slot);
   auto *wrapped=b.CreateICmpEQ(next,ConstantInt::get(u64,0));
   auto *thenTerm=SplitBlockAndInsertIfThen(wrapped,&*b.GetInsertPoint(),false);IRBuilder<> cold(thenTerm);cold.CreateCall(ov);
  }
  return PreservedAnalyses::none();
 }
};
extern "C" LLVM_ATTRIBUTE_WEAK PassPluginLibraryInfo llvmGetPassPluginInfo(){return {LLVM_PLUGIN_API_VERSION,"WorkMeter","1",[](PassBuilder &PB){PB.registerOptimizerLastEPCallback([](ModulePassManager &MPM,OptimizationLevel){MPM.addPass(WorkMeter());});}};}
