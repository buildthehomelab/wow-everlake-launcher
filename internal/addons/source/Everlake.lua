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
SlashCmdList.EVERLAKE = function()
    print("Everlake — " .. addon.client .. " client " .. addon.version)
end
