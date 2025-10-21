import { InitModule } from "@heroiclabs/nakama-runtime";
let NkInitModule: nkruntime.InitModule =
        function(ctx: nkruntime.Context, logger: nkruntime.Logger, nk: nkruntime.Nakama, initializer: nkruntime.Initializer) {
    logger.info("TypeScript module loaded.");
}

export default NkInitModule;
