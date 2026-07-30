# Windows CreateProcess 的命令行长度不足以直接传入 7zz 的全部对象文件。
# 此覆盖规则在 GNU make 展开阶段生成 Clang response file；Linux/macOS 同样可复现使用。

ZCR_LINK_RESPONSE = $(O)/zcr-link.rsp

$(PROGPATH): $(OBJS)
	$(file >$(ZCR_LINK_RESPONSE),$(LFLAGS_ALL))
	$(CXX) -o $(PROGPATH) @$(ZCR_LINK_RESPONSE)
