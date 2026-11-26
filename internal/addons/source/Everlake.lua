local _, addon = ...
local frame = CreateFrame("Frame")
frame:RegisterEvent("ADDON_LOADED")
frame:SetScript("OnEvent", function(_, _, name)
    if name ~= "Everlake" then return end
    EverlakeDB = EverlakeDB or {}
    EverlakeDB.client = addon.client
    EverlakeDB.version = addon.version
end)

SLASH_EVERLAKE1 = "/everlake"
SlashCmdList.EVERLAKE = function(msg)
    local cmd, arg = (msg or ""):lower():match("^%s*(%S*)%s*(.-)%s*$")
    if (cmd == "prof" or cmd == "professions") and addon.ProfessionsCommand then
        addon.ProfessionsCommand(arg)
        return
    end
    print("Everlake — " .. addon.client .. " client " .. addon.version)
    if addon.ProfessionsCommand then
        print("/everlake prof — show or hide every profession you know (also opens with the spellbook)")
    end
end
